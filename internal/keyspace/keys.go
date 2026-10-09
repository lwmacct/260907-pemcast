// Package keyspace defines the configurable etcd namespace and fixed target-first protocol paths.
package keyspace

import (
	"fmt"
	"path"
	"strings"
)

// ProtocolRoot is the fixed target-first v3 protocol path below the configured etcd prefix.
const ProtocolRoot = "/v3"

// DefaultPrefix is the default etcd namespace in front of ProtocolRoot.
const DefaultPrefix = "/pemcast"

// Keys builds etcd keys below one validated namespace prefix.
type Keys struct {
	prefix string
}

// NewKeys validates and normalizes an etcd namespace prefix.
func NewKeys(prefix string) (Keys, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return Keys{}, fmt.Errorf("etcd prefix must not be empty")
	}
	if !strings.HasPrefix(prefix, "/") {
		return Keys{}, fmt.Errorf("etcd prefix %q must be absolute", prefix)
	}

	for _, component := range strings.Split(prefix, "/") {
		if component == "." || component == ".." {
			return Keys{}, fmt.Errorf("etcd prefix %q has unsafe component %q", prefix, component)
		}
	}
	prefix = path.Clean(prefix)
	if prefix != "/" {
		for _, component := range strings.Split(strings.TrimPrefix(prefix, "/"), "/") {
			if !safePrefixComponent(component) {
				return Keys{}, fmt.Errorf("etcd prefix %q has unsafe component %q", prefix, component)
			}
		}
	}
	return Keys{prefix: prefix}, nil
}

// Default returns keys below DefaultPrefix.
func Default() Keys { return Keys{prefix: DefaultPrefix} }

func safePrefixComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		!strings.ContainsAny(component, `/\`)
}

// Prefix returns the normalized namespace prefix.
func (k Keys) Prefix() string { return k.prefix }

// Root returns the configured prefix plus the fixed v3 protocol root.
func (k Keys) Root() string { return path.Join(k.prefix, ProtocolRoot) }

// TargetPrefix returns the RBAC isolation boundary for one target.
func (k Keys) TargetPrefix(targetID string) string {
	return k.Root() + "/" + targetID + "/"
}

// ActiveKey returns one target's active pointer key below its target prefix.
func (k Keys) ActiveKey(targetID string) string {
	return k.Root() + "/" + targetID + "/active"
}

// BundleKey returns one immutable bundle path below its target prefix.
func (k Keys) BundleKey(targetID, generation string) string {
	return k.Root() + "/" + targetID + "/bundles/" + generation
}
