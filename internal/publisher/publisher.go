package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
)

const PlanSchema = "pemcast-publish/v2"

const (
	certificateName = "fullchain.pem"
	privateKeyName  = "privkey.pem"
)

type KV interface {
	Get(ctx context.Context, key string) (etcdsource.Value, error)
	CreateBundleAndSwapActive(
		ctx context.Context,
		bundleKey, bundleValue, activeKey, generation string,
		expected etcdsource.ActiveCondition,
	) (bool, error)
	SwapActive(ctx context.Context, activeKey, generation string, expected etcdsource.ActiveCondition) (bool, error)
}

type PlanOptions struct {
	TargetID        string
	CertificatePath string
	PrivateKeyPath  string
}

type ActiveState struct {
	TargetID string `json:"target-id"`
	Active   Active `json:"active"`
}

type Active struct {
	Exists      bool   `json:"exists"`
	Generation  string `json:"generation"`
	ModRevision int64  `json:"mod-revision"`
}

type Plan struct {
	Schema                    string `json:"schema"`
	TargetID                  string `json:"target-id"`
	Generation                string `json:"generation"`
	BundleSHA256              string `json:"bundle-sha256"`
	CertificatePath           string `json:"certificate-path"`
	PrivateKeyPath            string `json:"private-key-path"`
	CertificateSHA256         string `json:"certificate-sha256"`
	PrivateKeySHA256          string `json:"private-key-sha256"`
	ExpectedActiveGeneration  string `json:"expected-active-generation,omitempty"`
	ExpectedActiveModRevision int64  `json:"expected-active-mod-revision"`
	ExpectedActiveExists      bool   `json:"expected-active-exists"`
}

func Inspect(ctx context.Context, kv KV, targetID string) (ActiveState, error) {
	if err := validateTarget(targetID); err != nil {
		return ActiveState{}, err
	}
	value, err := kv.Get(ctx, etcdsource.ActiveKey(targetID))
	if err != nil {
		return ActiveState{}, fmt.Errorf("read active pointer: %w", err)
	}
	if value.Exists && !bundle.SafeName(value.Data) {
		return ActiveState{}, fmt.Errorf("active pointer %q is unsafe", value.Data)
	}
	return ActiveState{
		TargetID: targetID,
		Active: Active{
			Exists:      value.Exists,
			Generation:  value.Data,
			ModRevision: value.ModRevision,
		},
	}, nil
}

type ActivateOptions struct {
	TargetID   string
	Generation string
}

type localMaterial struct {
	manifest bundle.Manifest
	files    map[string][]byte
	digest   string
}

func CreatePlan(ctx context.Context, kv KV, options PlanOptions) (Plan, error) {
	if err := validateTarget(options.TargetID); err != nil {
		return Plan{}, err
	}
	material, certificatePath, privateKeyPath, err := readMaterial(options.CertificatePath, options.PrivateKeyPath)
	if err != nil {
		return Plan{}, err
	}
	if err := validateMaterial(material); err != nil {
		return Plan{}, err
	}
	if _, err := bundle.Encode(material.manifest); err != nil {
		return Plan{}, err
	}
	expected, err := captureActive(ctx, kv, options.TargetID)
	if err != nil {
		return Plan{}, err
	}

	return Plan{
		Schema:                    PlanSchema,
		TargetID:                  options.TargetID,
		Generation:                bundle.Generation(material.digest),
		BundleSHA256:              material.digest,
		CertificatePath:           certificatePath,
		PrivateKeyPath:            privateKeyPath,
		CertificateSHA256:         hash(material.files[certificateName]),
		PrivateKeySHA256:          hash(material.files[privateKeyName]),
		ExpectedActiveGeneration:  expected.Generation,
		ExpectedActiveModRevision: expected.ModRevision,
		ExpectedActiveExists:      expected.Exists,
	}, nil
}

func DecodePlan(data []byte) (Plan, error) {
	var plan Plan
	if err := json.Unmarshal(data, &plan, json.RejectUnknownMembers(true)); err != nil {
		return Plan{}, fmt.Errorf("decode publish plan: %w", err)
	}
	if plan.Schema != PlanSchema {
		return Plan{}, fmt.Errorf("unsupported publish plan schema %q", plan.Schema)
	}
	if err := validateTarget(plan.TargetID); err != nil {
		return Plan{}, err
	}
	if err := validateActiveCondition(plan.ExpectedActiveGeneration, plan.ExpectedActiveModRevision, plan.ExpectedActiveExists); err != nil {
		return Plan{}, err
	}
	if plan.Generation != bundle.Generation(plan.BundleSHA256) {
		return Plan{}, fmt.Errorf("plan generation does not match bundle digest")
	}
	return plan, nil
}

func Apply(ctx context.Context, kv KV, plan Plan) error {
	if err := validateTarget(plan.TargetID); err != nil {
		return err
	}
	if err := validateActiveCondition(plan.ExpectedActiveGeneration, plan.ExpectedActiveModRevision, plan.ExpectedActiveExists); err != nil {
		return err
	}
	material, _, _, err := readMaterial(plan.CertificatePath, plan.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("reload local certificate material: %w", err)
	}
	if material.digest != plan.BundleSHA256 ||
		hash(material.files[certificateName]) != plan.CertificateSHA256 ||
		hash(material.files[privateKeyName]) != plan.PrivateKeySHA256 {
		return fmt.Errorf("local certificate material changed after publish plan")
	}
	if err := validateMaterial(material); err != nil {
		return fmt.Errorf("reload local certificate material: %w", err)
	}
	encoded, err := bundle.Encode(material.manifest)
	if err != nil {
		return err
	}

	bundleKey := etcdsource.BundleKey(plan.TargetID, plan.Generation)
	activeKey := etcdsource.ActiveKey(plan.TargetID)
	existing, err := kv.Get(ctx, bundleKey)
	if err != nil {
		return fmt.Errorf("read existing bundle: %w", err)
	}
	expected := etcdsource.ActiveCondition{
		Generation:  plan.ExpectedActiveGeneration,
		ModRevision: plan.ExpectedActiveModRevision,
		Exists:      plan.ExpectedActiveExists,
	}
	if !existing.Exists {
		updated, err := kv.CreateBundleAndSwapActive(ctx, bundleKey, string(encoded), activeKey, plan.Generation, expected)
		if err != nil {
			return err
		}
		if !updated {
			return fmt.Errorf("remote state changed after publish plan")
		}
		return nil
	}
	if existing.Data != string(encoded) {
		return fmt.Errorf("content-addressed generation %q contains different data", plan.Generation)
	}
	if expected.Exists && expected.Generation == plan.Generation {
		return nil
	}
	updated, err := kv.SwapActive(ctx, activeKey, plan.Generation, expected)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("active pointer changed after publish plan")
	}
	return nil
}

func Activate(ctx context.Context, kv KV, options ActivateOptions) error {
	if err := validateTarget(options.TargetID); err != nil {
		return err
	}
	if !bundle.SafeName(options.Generation) {
		return fmt.Errorf("generation %q is unsafe", options.Generation)
	}
	expected, err := captureActive(ctx, kv, options.TargetID)
	if err != nil {
		return err
	}
	bundleKey := etcdsource.BundleKey(options.TargetID, options.Generation)
	value, err := kv.Get(ctx, bundleKey)
	if err != nil {
		return fmt.Errorf("read existing bundle: %w", err)
	}
	if !value.Exists {
		return fmt.Errorf("generation %q does not exist", options.Generation)
	}
	manifest, files, digest, err := bundle.Decode([]byte(value.Data))
	if err != nil {
		return fmt.Errorf("decode existing bundle: %w", err)
	}
	if err := bundle.ValidateGeneration(options.Generation, digest); err != nil {
		return err
	}
	if len(manifest.Pairs) != 1 {
		return fmt.Errorf("activate requires exactly one certificate pair")
	}
	pair := manifest.Pairs[0]
	if _, err := bundle.ValidateKeyPair(files[pair.Certificate], files[pair.PrivateKey]); err != nil {
		return err
	}
	if expected.Exists && expected.Generation == options.Generation {
		return nil
	}

	updated, err := kv.SwapActive(ctx, etcdsource.ActiveKey(options.TargetID), options.Generation, expected)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("active pointer changed during activation")
	}
	return nil
}

func captureActive(ctx context.Context, kv KV, targetID string) (etcdsource.ActiveCondition, error) {
	value, err := kv.Get(ctx, etcdsource.ActiveKey(targetID))
	if err != nil {
		return etcdsource.ActiveCondition{}, fmt.Errorf("read active pointer: %w", err)
	}
	if value.Exists && !bundle.SafeName(value.Data) {
		return etcdsource.ActiveCondition{}, fmt.Errorf("active pointer %q is unsafe", value.Data)
	}
	return etcdsource.ActiveCondition{
		Generation:  value.Data,
		ModRevision: value.ModRevision,
		Exists:      value.Exists,
	}, nil
}

func readMaterial(certificatePath, privateKeyPath string) (localMaterial, string, string, error) {
	certificate, err := readFile(certificatePath)
	if err != nil {
		return localMaterial{}, "", "", fmt.Errorf("read certificate: %w", err)
	}
	privateKey, err := readFile(privateKeyPath)
	if err != nil {
		return localMaterial{}, "", "", fmt.Errorf("read private key: %w", err)
	}
	manifest, digest := bundle.NewTLSManifest(certificate, privateKey, certificateName, privateKeyName)
	return localMaterial{
		manifest: manifest,
		files:    map[string][]byte{certificateName: certificate, privateKeyName: privateKey},
		digest:   digest,
	}, absoluteForPlan(certificatePath), absoluteForPlan(privateKeyPath), nil
}

func validateMaterial(material localMaterial) error {
	_, err := bundle.ValidateKeyPair(material.files[certificateName], material.files[privateKeyName])
	return err
}

func readFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func absoluteForPlan(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return absolute
}

func validateTarget(targetID string) error {
	if !bundle.SafeName(targetID) {
		return fmt.Errorf("target id %q is unsafe", targetID)
	}
	return nil
}

func validateActiveCondition(generation string, modRevision int64, exists bool) error {
	if exists {
		if generation == "" || !bundle.SafeName(generation) {
			return fmt.Errorf("expected active generation %q is unsafe", generation)
		}
		if modRevision <= 0 {
			return fmt.Errorf("expected active ModRevision must be positive when the active pointer exists")
		}
		return nil
	}
	if generation != "" || modRevision != 0 {
		return fmt.Errorf("expected active generation and ModRevision must be empty when the active pointer is absent")
	}
	return nil
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
