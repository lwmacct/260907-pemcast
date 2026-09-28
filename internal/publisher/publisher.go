package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

const maxFileBytes = 4 << 20

type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	PutIf(ctx context.Context, key, value, expected string, expectedExists bool) (bool, error)
}

type Options struct {
	RootPrefix         string
	TargetID           string
	Generation         string
	CertificatePath    string
	PrivateKeyPath     string
	PreviousGeneration string
	AllowMissingActive bool
	ActivateExisting   bool
}

type material struct {
	manifest bundle.Manifest
	files    map[string][]byte
	digest   string
}

func Publish(ctx context.Context, kv KV, options Options) error {
	if err := options.validate(); err != nil {
		return err
	}
	rootPrefix := cleanRoot(options.RootPrefix)
	activeKey := activeKey(rootPrefix, options.TargetID)
	current, exists, err := kv.Get(ctx, activeKey)
	if err != nil {
		return fmt.Errorf("read active pointer: %w", err)
	}
	activeExists := exists
	expected := current
	switch {
	case options.PreviousGeneration != "":
		if !activeExists || current != options.PreviousGeneration {
			return fmt.Errorf("active pointer is %q, expected %q", pointerValue(exists, current), options.PreviousGeneration)
		}
	case activeExists:
		if !bundle.SafeName(current) {
			return fmt.Errorf("active pointer %q is unsafe", current)
		}
	case !options.AllowMissingActive:
		return fmt.Errorf("active pointer is missing and --allow-missing-active was not set")
	}

	bundleRootKey := bundleRoot(rootPrefix, options.TargetID, options.Generation)
	fileKeys := []string{
		bundleRootKey + "/files/fullchain.pem",
		bundleRootKey + "/files/privkey.pem",
	}
	manifestKey := bundleRootKey + "/manifest.json"

	var local material
	if options.ActivateExisting {
		for _, key := range append(fileKeys, manifestKey) {
			if _, exists, err := kv.Get(ctx, key); err != nil {
				return fmt.Errorf("inspect existing generation: %w", err)
			} else if !exists {
				return fmt.Errorf("existing generation is missing %q", key)
			}
		}
	} else {
		local, err = readMaterial(options)
		if err != nil {
			return fmt.Errorf("read certificate material: %w", err)
		}
		manifestBytes, err := json.Marshal(local.manifest)
		if err != nil {
			return fmt.Errorf("encode manifest: %w", err)
		}
		values := []struct {
			key   string
			name  string
			value string
		}{
			{key: fileKeys[0], name: "fullchain.pem", value: string(local.files["fullchain.pem"])},
			{key: fileKeys[1], name: "privkey.pem", value: string(local.files["privkey.pem"])},
		}
		for _, item := range values {
			created, err := kv.PutIf(ctx, item.key, item.value, "", false)
			if err != nil {
				return fmt.Errorf("create bundle file %q: %w", item.name, err)
			}
			if !created {
				return fmt.Errorf("generation %q already exists", options.Generation)
			}
		}
		created, err := kv.PutIf(ctx, manifestKey, string(manifestBytes), "", false)
		if err != nil {
			return fmt.Errorf("create manifest: %w", err)
		}
		if !created {
			return fmt.Errorf("generation %q already exists", options.Generation)
		}
	}

	fetched := make(map[string][]byte, len(fileKeys))
	for _, key := range fileKeys {
		value, exists, err := kv.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("verify bundle file %q: %w", key, err)
		}
		if !exists {
			return fmt.Errorf("verify bundle file %q: value missing", key)
		}
		name := key[strings.LastIndex(key, "/")+1:]
		fetched[name] = []byte(value)
	}
	manifestValue, exists, err := kv.Get(ctx, manifestKey)
	if err != nil {
		return fmt.Errorf("verify manifest: %w", err)
	}
	if !exists {
		return fmt.Errorf("verify manifest: value missing")
	}
	fetchedManifest, err := bundle.ParseManifest([]byte(manifestValue))
	if err != nil {
		return fmt.Errorf("verify manifest: %w", err)
	}
	digest, err := bundle.VerifyFiles(fetchedManifest, fetched)
	if err != nil || (!options.ActivateExisting && digest != local.digest) {
		if err == nil {
			err = fmt.Errorf("digest %q does not match local digest %q", digest, local.digest)
		}
		return fmt.Errorf("verify immutable generation: %w", err)
	}

	updated, err := kv.PutIf(ctx, activeKey, options.Generation, expected, activeExists)
	if err != nil {
		return fmt.Errorf("update active pointer: %w", err)
	}
	if !updated {
		return fmt.Errorf("active pointer changed during publication")
	}
	return nil
}

func (o Options) validate() error {
	if !bundle.SafeName(o.TargetID) {
		return fmt.Errorf("target id %q is unsafe", o.TargetID)
	}
	if !bundle.SafeName(o.Generation) {
		return fmt.Errorf("generation %q is unsafe", o.Generation)
	}
	if o.PreviousGeneration != "" && !bundle.SafeName(o.PreviousGeneration) {
		return fmt.Errorf("previous generation %q is unsafe", o.PreviousGeneration)
	}
	return nil
}

func readMaterial(options Options) (material, error) {
	certificate, err := readFile(options.CertificatePath)
	if err != nil {
		return material{}, fmt.Errorf("read certificate: %w", err)
	}
	privateKey, err := readFile(options.PrivateKeyPath)
	if err != nil {
		return material{}, fmt.Errorf("read private key: %w", err)
	}
	if _, err := bundle.ValidateKeyPair(certificate, privateKey); err != nil {
		return material{}, err
	}

	files := map[string][]byte{"fullchain.pem": certificate, "privkey.pem": privateKey}
	manifest := bundle.Manifest{
		Schema: bundle.SchemaV1,
		Files: []bundle.ManifestFile{
			{Name: "fullchain.pem", Kind: bundle.KindCertificate, SHA256: hash(certificate)},
			{Name: "privkey.pem", Kind: bundle.KindPrivateKey, SHA256: hash(privateKey)},
		},
		Pairs: []bundle.Pair{{Certificate: "fullchain.pem", PrivateKey: "privkey.pem"}},
	}
	digest, err := bundle.VerifyFiles(manifest, files)
	if err != nil {
		return material{}, err
	}
	return material{manifest: manifest, files: files, digest: digest}, nil
}

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("size %d exceeds %d bytes", len(data), maxFileBytes)
	}
	return data, nil
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func cleanRoot(value string) string {
	if value == "" {
		return "/pemcast/v1"
	}
	return path.Clean("/" + strings.Trim(value, "/"))
}

func activeKey(rootPrefix, targetID string) string {
	return path.Join(rootPrefix, "active", targetID)
}

func bundleRoot(rootPrefix, targetID, generation string) string {
	return path.Join(rootPrefix, "bundles", targetID, generation)
}

func pointerValue(exists bool, value string) string {
	if !exists {
		return "<missing>"
	}
	return value
}
