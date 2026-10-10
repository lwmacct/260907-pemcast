// Package seed materializes a validated local v6 pack without contacting etcd.
package seed

import (
	"fmt"
	"time"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
	"github.com/lwmacct/260907-pemcast/internal/pack"
)

// Options controls local activation of one validated pack.
type Options struct {
	Target config.Target
	Pack   pack.Result
	Prefix string
	Force  bool
	Now    time.Time
}

// Result reports the local materialization outcome.
type Result struct {
	Changed    bool
	Generation string
	Digest     string
	ReleaseDir string
	CurrentDir string
}

// Material validates a pack against one configured target and returns the
// decoded deployment material. It performs no local filesystem writes.
func Material(options Options) (*bundle.Material, error) {
	if options.Now.IsZero() {
		options.Now = time.Now()
	}

	if err := pack.Validate(options.Pack); err != nil {
		return nil, fmt.Errorf("validate pack: %w", err)
	}
	keys, err := keyspace.NewKeys(options.Prefix)
	if err != nil {
		return nil, fmt.Errorf("validate etcd prefix: %w", err)
	}
	metadata := options.Pack.Metadata
	if metadata.EtcdPrefix != keys.Prefix() {
		return nil, fmt.Errorf(
			"pack etcd prefix %q does not match configured prefix %q",
			metadata.EtcdPrefix, keys.Prefix(),
		)
	}
	if metadata.TargetID != options.Target.ID {
		return nil, fmt.Errorf(
			"pack target %q does not match requested target %q",
			metadata.TargetID, options.Target.ID,
		)
	}
	if metadata.Type != options.Target.Type {
		return nil, fmt.Errorf(
			"pack type %q does not match requested target type %q",
			metadata.Type, options.Target.Type,
		)
	}

	manifest, files, digest, err := bundle.Decode(options.Pack.Bundle)
	if err != nil {
		return nil, fmt.Errorf("decode pack bundle: %w", err)
	}
	if digest != metadata.BundleSHA256 {
		return nil, fmt.Errorf("pack bundle digest does not match decoded bundle")
	}
	certificates, leaf, err := manifest.ValidateFiles(files)
	if err != nil {
		return nil, fmt.Errorf("validate target certificate material: %w", err)
	}
	material := &bundle.Material{
		TargetID: options.Target.ID, Generation: metadata.Generation,
		Manifest: manifest, Files: files, Digest: digest,
		Certificates: certificates, Leaf: leaf,
	}
	if err := bundle.ValidatePolicy(material, bundle.Policy{
		Now: options.Now, RejectExpired: options.Target.Validation.RejectExpired,
		MinimumValidity: options.Target.Validation.MinimumValidity,
		ServerNames:     options.Target.Validation.ServerNames,
	}); err != nil {
		return nil, fmt.Errorf("validate target certificate validity: %w", err)
	}
	for _, mapping := range options.Target.Output.Mappings {
		if _, ok := files[mapping.Remote]; !ok {
			return nil, fmt.Errorf("mapped pack file %q is missing", mapping.Remote)
		}
	}

	return material, nil
}

// Apply validates a pack against one configured target and activates its local
// release. It never reads or writes agent reconciliation state. The caller must
// already hold the target output-root lock.
func Apply(options Options, deployer *deploy.Deployer) (Result, error) {
	if deployer == nil {
		deployer = deploy.New()
	}
	material, err := Material(options)
	if err != nil {
		return Result{}, err
	}
	currentDigest, err := deployer.CurrentDigest(options.Target.Output)
	if err != nil {
		return Result{}, fmt.Errorf("read current release digest: %w", err)
	}
	if currentDigest != "" && currentDigest != material.Digest && !options.Force {
		return Result{}, fmt.Errorf(
			"current release digest %q differs from pack digest %q; retry with --force for explicit local rescue",
			currentDigest, material.Digest,
		)
	}

	activation, err := deployer.Activate(material, options.Target.Output)
	if err != nil {
		return Result{}, fmt.Errorf("activate local release: %w", err)
	}
	return Result{
		Changed: activation.Changed, Generation: material.Generation, Digest: material.Digest,
		ReleaseDir: activation.ReleaseDir, CurrentDir: activation.CurrentDir,
	}, nil
}
