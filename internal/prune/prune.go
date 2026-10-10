// Package prune reports and removes expired non-active identity bundles.
package prune

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
)

// KV is the exact etcd surface needed by bundle lifecycle reporting.
type KV interface {
	Prefix() string
	SnapshotActivePrefix(ctx context.Context, activePrefix string) (etcdsource.PrefixSnapshot, error)
	ListBundleRecords(ctx context.Context, bundlePrefix string) ([]etcdsource.BundleRecord, error)
	DeleteBundleIfActiveUnchanged(
		ctx context.Context,
		bundleKey string,
		activeKey string,
		expected etcdsource.ActiveCondition,
	) (bool, error)
}

// Options controls one prefix scan.
type Options struct {
	Prefix    string
	Now       time.Time
	Retention time.Duration
	Delete    bool
}

// Item reports one bundle's lifecycle decision.
type Item struct {
	TargetID         string     `json:"target-id"`
	Generation       string     `json:"generation"`
	Type             string     `json:"type"`
	EarliestNotAfter *time.Time `json:"earliest-not-after,omitempty"`
	Eligible         bool       `json:"eligible"`
	Status           string     `json:"status"`
}

// Result reports a complete prefix scan.
type Result struct {
	Prefix    string        `json:"prefix"`
	Retention time.Duration `json:"retention"`
	Scanned   int           `json:"scanned"`
	Eligible  int           `json:"eligible"`
	Deleted   int           `json:"deleted"`
	Items     []Item        `json:"items"`
}

// Prune scans bundles, reports candidates, and optionally deletes eligible
// non-active identity bundles with an exact-key conditional transaction.
func Prune(ctx context.Context, kv KV, options Options) (Result, error) {
	if options.Retention < 0 {
		return Result{}, fmt.Errorf("retention must not be negative")
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	keys, err := keyspace.NewKeys(options.Prefix)
	if err != nil {
		return Result{}, err
	}
	if kv.Prefix() != keys.Prefix() {
		return Result{}, fmt.Errorf(
			"prune client prefix %q does not match configured prefix %q",
			kv.Prefix(), keys.Prefix(),
		)
	}

	active, err := kv.SnapshotActivePrefix(ctx, keys.ActivePrefix())
	if err != nil {
		return Result{}, fmt.Errorf("snapshot active pointers: %w", err)
	}
	records, err := kv.ListBundleRecords(ctx, keys.BundlePrefix())
	if err != nil {
		return Result{}, fmt.Errorf("list bundles: %w", err)
	}

	result := Result{Prefix: keys.Prefix(), Retention: options.Retention, Scanned: len(records)}
	for _, record := range records {
		item, err := inspectRecord(record, active.Active[record.TargetID], options.Now, options.Retention)
		if err != nil {
			return result, fmt.Errorf("inspect bundle %q: %w", record.Key, err)
		}
		if item.Eligible {
			result.Eligible++
		}
		if !item.Eligible || !options.Delete {
			result.Items = append(result.Items, item)
			continue
		}

		deleted, err := kv.DeleteBundleIfActiveUnchanged(
			ctx, record.Key, keys.ActiveKey(record.TargetID), etcdsource.ActiveCondition{
				Generation:  active.Active[record.TargetID].Generation,
				ModRevision: active.Active[record.TargetID].ModRevision,
				Exists:      true,
			},
		)
		if err != nil {
			return result, fmt.Errorf("delete bundle %q: %w", record.Key, err)
		}
		if !deleted {
			return result, fmt.Errorf("delete bundle %q: active pointer changed; retry prune", record.Key)
		}
		result.Deleted++
		item.Status = "deleted"
		result.Items = append(result.Items, item)
	}
	sort.Slice(result.Items, func(i, j int) bool {
		if result.Items[i].TargetID != result.Items[j].TargetID {
			return result.Items[i].TargetID < result.Items[j].TargetID
		}
		return result.Items[i].Generation < result.Items[j].Generation
	})
	return result, nil
}

func inspectRecord(
	record etcdsource.BundleRecord,
	active etcdsource.ActivePointer,
	now time.Time,
	retention time.Duration,
) (Item, error) {
	manifest, files, digest, err := bundle.Decode(record.Data)
	if err != nil {
		return Item{}, fmt.Errorf("decode bundle: %w", err)
	}
	certificates, _, err := manifest.ValidateFiles(files)
	if err != nil {
		return Item{}, fmt.Errorf("validate bundle semantics: %w", err)
	}
	if err := bundle.ValidateGeneration(record.Generation, digest); err != nil {
		return Item{}, err
	}

	item := Item{
		TargetID: record.TargetID, Generation: record.Generation, Type: manifest.Type,
	}
	if active.Generation == record.Generation {
		item.Status = "active"
		return item, nil
	}
	if manifest.Type == bundle.TypeTrust {
		item.Status = "trust-skipped"
		return item, nil
	}
	if active.Generation == "" {
		item.Status = "no-active-pointer"
		return item, nil
	}
	if len(certificates) == 0 {
		return item, fmt.Errorf("identity bundle contains no parsed certificates")
	}

	earliest := certificates[0].NotAfter
	for _, certificate := range certificates[1:] {
		if certificate.NotAfter.Before(earliest) {
			earliest = certificate.NotAfter
		}
	}
	item.EarliestNotAfter = &earliest
	if now.Sub(earliest) > retention {
		item.Eligible = true
		item.Status = "would-delete"
		return item, nil
	}
	item.Status = "retained"
	return item, nil
}
