// Package pack builds local immutable v6 certificate bundles and etcdctl input.
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

const MetadataSchema = "pemcast-pack/v6"

// Options describes one local packing operation. Packing never contacts etcd.
type Options struct {
	Type            string
	TargetID        string
	EtcdPrefix      string
	CertificatePath string
	PrivateKeyPath  string
	CAPath          string
}

// Metadata describes a packed bundle without exposing its private key.
type Metadata struct {
	Schema            string                  `json:"schema"`
	Type              string                  `json:"type"`
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

// MetadataFile names one source file role and raw-content digest.
type MetadataFile struct {
	Role   string `json:"role"`
	SHA256 string `json:"sha256"`
}

// Result contains deterministic local publication artifacts.
type Result struct {
	Metadata Metadata
	Bundle   []byte
	StageTxn []byte
}

// Build validates local certificate material and derives every remote v6 artifact.
func Build(options Options) (Result, error) {
	if !bundle.SafeName(options.TargetID) {
		return Result{}, fmt.Errorf("target id %q is unsafe", options.TargetID)
	}
	keys, err := keyspace.NewKeys(options.EtcdPrefix)
	if err != nil {
		return Result{}, err
	}

	var manifest bundle.Manifest
	var digest string
	switch options.Type {
	case bundle.TypeTLSServer, bundle.TypeTLSClient:
		certificate, err := readFile(options.CertificatePath)
		if err != nil {
			return Result{}, fmt.Errorf("read certificate: %w", err)
		}
		privateKey, err := readFile(options.PrivateKeyPath)
		if err != nil {
			return Result{}, fmt.Errorf("read private key: %w", err)
		}
		manifest, digest = bundle.NewTLSManifest(options.Type, certificate, privateKey)
	case bundle.TypeTrust:
		certificate, err := readFile(options.CAPath)
		if err != nil {
			return Result{}, fmt.Errorf("read CA bundle: %w", err)
		}
		manifest, digest = bundle.NewTrustManifest(certificate)
	default:
		return Result{}, fmt.Errorf("unsupported certificate bundle type %q", options.Type)
	}

	encoded, err := bundle.Encode(manifest)
	if err != nil {
		return Result{}, err
	}
	generation := bundle.Generation(digest)
	metadata := Metadata{
		Schema:            MetadataSchema,
		Type:              options.Type,
		EtcdPrefix:        keys.Prefix(),
		TargetID:          options.TargetID,
		Generation:        generation,
		BundleSHA256:      digest,
		BundleValueSHA256: hash(encoded),
		EncodedSize:       len(encoded),
		ActiveKey:         keys.ActiveKey(options.TargetID),
		BundleKey:         keys.BundleKey(options.TargetID, generation),
		Files:             metadataFiles(manifest),
	}
	return Result{
		Metadata: metadata,
		Bundle:   encoded,
		StageTxn: []byte(stageTransaction(metadata.BundleKey, encoded)),
	}, nil
}

// Read loads and strictly validates a pack directory. The stage transaction is
// intentionally optional because it is only used by the manual etcdctl path.
func Read(directory string) (Result, error) {
	if strings.TrimSpace(directory) == "" {
		return Result{}, fmt.Errorf("pack directory is empty")
	}
	metadataData, err := os.ReadFile(filepath.Join(directory, "metadata.json"))
	if err != nil {
		return Result{}, fmt.Errorf("read pack metadata: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(metadataData, &metadata, json.RejectUnknownMembers(true)); err != nil {
		return Result{}, fmt.Errorf("decode pack metadata: %w", err)
	}
	bundleData, err := os.ReadFile(filepath.Join(directory, "bundle.json"))
	if err != nil {
		return Result{}, fmt.Errorf("read pack bundle: %w", err)
	}
	result := Result{Metadata: metadata, Bundle: bundleData}
	if err := Validate(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// Validate independently derives and checks every pack field used by publish.
func Validate(result Result) error {
	metadata := result.Metadata
	if metadata.Schema != MetadataSchema {
		return fmt.Errorf("unsupported pack metadata schema %q", metadata.Schema)
	}
	keys, err := keyspace.NewKeys(metadata.EtcdPrefix)
	if err != nil {
		return err
	}
	if !bundle.SafeName(metadata.TargetID) {
		return fmt.Errorf("pack target id %q is unsafe", metadata.TargetID)
	}
	if metadata.BundleValueSHA256 != hash(result.Bundle) {
		return fmt.Errorf("pack bundle bytes do not match bundle-value-sha256")
	}
	if metadata.EncodedSize != len(result.Bundle) {
		return fmt.Errorf(
			"pack encoded size is %d but bundle contains %d bytes",
			metadata.EncodedSize, len(result.Bundle),
		)
	}

	manifest, files, digest, err := bundle.Decode(result.Bundle)
	if err != nil {
		return fmt.Errorf("decode pack bundle: %w", err)
	}
	if digest != metadata.BundleSHA256 {
		return fmt.Errorf("pack bundle digest does not match bundle-sha256")
	}
	if err := bundle.ValidateGeneration(metadata.Generation, digest); err != nil {
		return fmt.Errorf("pack generation is invalid: %w", err)
	}
	if expectedActiveKey := keys.ActiveKey(metadata.TargetID); metadata.ActiveKey != expectedActiveKey {
		return fmt.Errorf("pack active key %q does not match expected %q", metadata.ActiveKey, expectedActiveKey)
	}
	expectedBundleKey := keys.BundleKey(metadata.TargetID, metadata.Generation)
	if metadata.BundleKey != expectedBundleKey {
		return fmt.Errorf("pack bundle key %q does not match expected %q", metadata.BundleKey, expectedBundleKey)
	}
	if metadata.Type != manifest.Type {
		return fmt.Errorf("pack metadata type %q does not match bundle type %q", metadata.Type, manifest.Type)
	}

	expectedRoles := expectedFileRoles(manifest.Type)
	if expectedRoles == nil || len(files) != len(expectedRoles) ||
		len(manifest.Files) != len(expectedRoles) || len(metadata.Files) != len(expectedRoles) {
		return fmt.Errorf("pack type %q has an invalid file set", manifest.Type)
	}
	for _, file := range manifest.Files {
		expected, ok := metadata.Files[file.Name]
		if !ok {
			return fmt.Errorf("pack metadata is missing file %q", file.Name)
		}
		if expected.Role != file.Role || expected.SHA256 != file.SHA256 {
			return fmt.Errorf("pack metadata does not match bundle file %q", file.Name)
		}
		if wanted, ok := expectedRoles[file.Name]; !ok || file.Role != wanted {
			return fmt.Errorf("pack bundle file %q has an invalid role for type %q", file.Name, manifest.Type)
		}
	}
	return nil
}

// Write creates a new output directory containing the bundle, metadata, and
// etcdctl stage transaction. It refuses to replace an existing directory.
func Write(outputDir string, result Result) error {
	outputDir = filepath.Clean(outputDir)
	if outputDir == "." || outputDir == string(filepath.Separator) || !filepath.IsAbs(outputDir) {
		return fmt.Errorf("output directory %q must be a clean absolute non-root path", outputDir)
	}
	if err := Validate(result); err != nil {
		return fmt.Errorf("refuse invalid pack: %w", err)
	}
	expectedStageTxn := stageTransaction(result.Metadata.BundleKey, result.Bundle)
	if len(result.StageTxn) == 0 {
		result.StageTxn = []byte(expectedStageTxn)
	} else if string(result.StageTxn) != expectedStageTxn {
		return fmt.Errorf("pack stage transaction does not match bundle and bundle key")
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
		return fmt.Errorf("create file %q: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write file %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync file %q: %w", path, err)
	}
	return file.Close()
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func metadataFiles(manifest bundle.Manifest) map[string]MetadataFile {
	files := make(map[string]MetadataFile, len(manifest.Files))
	for _, file := range manifest.Files {
		files[file.Name] = MetadataFile{Role: file.Role, SHA256: file.SHA256}
	}
	return files
}

func expectedFileRoles(bundleType string) map[string]string {
	switch bundleType {
	case bundle.TypeTLSServer, bundle.TypeTLSClient:
		return map[string]string{
			bundle.NameCertificateChain: bundle.RoleCertificateChain,
			bundle.NamePrivateKey:       bundle.RolePrivateKey,
		}
	case bundle.TypeTrust:
		return map[string]string{bundle.NameCABundle: bundle.RoleCACertificate}
	default:
		return nil
	}
}
