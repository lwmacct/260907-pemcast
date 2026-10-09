// Package pack builds local immutable v5 certificate bundles and etcdctl input.
package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
)

const MetadataSchema = "pemcast-pack/v5"

const (
	certificateName = "fullchain.pem"
	privateKeyName  = "privkey.pem"
)

// Options describes one local packing operation. Packing never contacts etcd.
type Options struct {
	TargetID        string
	EtcdPrefix      string
	CertificatePath string
	PrivateKeyPath  string
}

// Metadata describes a packed bundle without exposing its private key.
type Metadata struct {
	Schema            string                  `json:"schema"`
	EtcdPrefix        string                  `json:"etcd-prefix"`
	TargetID          string                  `json:"target-id"`
	Generation        string                  `json:"generation"`
	BundleSHA256      string                  `json:"bundle-sha256"`
	BundleValueSHA256 string                  `json:"bundle-value-sha256"`
	EncodedSize       int                     `json:"encoded-size"`
	ActiveKey         string                  `json:"active-key"`
	BundleKey         string                  `json:"bundle-key"`
	Files             map[string]MetadataFile `json:"files"`
}

// MetadataFile names one source file kind and raw-content digest.
type MetadataFile struct {
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

// Result contains deterministic local publication artifacts.
type Result struct {
	Metadata Metadata
	Bundle   []byte
	StageTxn []byte
}

// Build validates local TLS material and derives every remote v5 artifact.
func Build(options Options) (Result, error) {
	if !bundle.SafeName(options.TargetID) {
		return Result{}, fmt.Errorf("target id %q is unsafe", options.TargetID)
	}
	keys, err := keyspace.NewKeys(options.EtcdPrefix)
	if err != nil {
		return Result{}, err
	}

	certificate, err := readFile(options.CertificatePath)
	if err != nil {
		return Result{}, fmt.Errorf("read certificate: %w", err)
	}
	privateKey, err := readFile(options.PrivateKeyPath)
	if err != nil {
		return Result{}, fmt.Errorf("read private key: %w", err)
	}
	if _, err := bundle.ValidateKeyPair(certificate, privateKey); err != nil {
		return Result{}, err
	}

	manifest, digest := bundle.NewTLSManifest(certificate, privateKey, certificateName, privateKeyName)
	encoded, err := bundle.Encode(manifest)
	if err != nil {
		return Result{}, err
	}
	generation := bundle.Generation(digest)
	metadata := Metadata{
		Schema:            MetadataSchema,
		EtcdPrefix:        keys.Prefix(),
		TargetID:          options.TargetID,
		Generation:        generation,
		BundleSHA256:      digest,
		BundleValueSHA256: hash(encoded),
		EncodedSize:       len(encoded),
		ActiveKey:         keys.ActiveKey(options.TargetID),
		BundleKey:         keys.BundleKey(options.TargetID, generation),
		Files: map[string]MetadataFile{
			certificateName: {Kind: bundle.KindCertificate, SHA256: hash(certificate)},
			privateKeyName:  {Kind: bundle.KindPrivateKey, SHA256: hash(privateKey)},
		},
	}
	return Result{
		Metadata: metadata,
		Bundle:   encoded,
		StageTxn: []byte(stageTransaction(metadata.BundleKey, encoded)),
	}, nil
}

// Write creates a new output directory containing the bundle, metadata, and
// etcdctl stage transaction. It refuses to replace an existing directory.
func Write(outputDir string, result Result) error {
	outputDir = filepath.Clean(outputDir)
	if outputDir == "." || outputDir == string(filepath.Separator) || !filepath.IsAbs(outputDir) {
		return fmt.Errorf("output directory %q must be a clean absolute non-root path", outputDir)
	}
	if _, err := os.Stat(outputDir); err == nil {
		return fmt.Errorf("output directory %q already exists", outputDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output directory: %w", err)
	}

	parent := filepath.Dir(outputDir)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("create output parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".pemcast-pack-*")
	if err != nil {
		return fmt.Errorf("create staging output: %w", err)
	}
	keepStaging := true
	defer func() {
		if keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	metadata, err := json.Marshal(result.Metadata)
	if err != nil {
		return fmt.Errorf("encode pack metadata: %w", err)
	}
	if err := writeFile(filepath.Join(staging, "bundle.json"), result.Bundle, 0o600); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(staging, "metadata.json"), append(metadata, '\n'), 0o600); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(staging, "stage.txn"), result.StageTxn, 0o600); err != nil {
		return err
	}
	if err := os.Rename(staging, outputDir); err != nil {
		return fmt.Errorf("publish pack output: %w", err)
	}
	keepStaging = false
	return nil
}

func stageTransaction(bundleKey string, bundleValue []byte) string {
	key := strconv.Quote(bundleKey)
	return strings.Join([]string{
		fmt.Sprintf("create(%s) = %q", key, "0"),
		"",
		fmt.Sprintf("put %s %s", key, strconv.Quote(string(bundleValue))),
		"",
		"",
		"",
	}, "\n")
}

func readFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is empty")
	}
	return os.ReadFile(path)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", filepath.Base(path), err)
	}
	return nil
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
