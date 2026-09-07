// Package config provides pemcast application configuration.
package config

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lwmacct/251207-go-pkg-cfgm/pkg/cfgm"
)

const AppName = "pemcast"

// Config is grouped by CLI subcommand so cfgm can trim command prefixes.
type Config struct {
	Agent Agent `json:"agent" desc:"pemcast agent configuration"`
}

// Agent configures the long-running or one-shot synchronization agent.
type Agent struct {
	Once          bool     `json:"once"           desc:"synchronize once and exit"`
	DryRun        bool     `json:"dry-run"        desc:"validate changes without writing files or running hooks"`
	StateDir      string   `json:"state-dir"      desc:"local state and lock directory"`
	MaxConcurrent int      `json:"max-concurrent" desc:"maximum targets reconciled concurrently"`
	Etcd          Etcd     `json:"etcd"           desc:"etcd client configuration"`
	Watch         Watch    `json:"watch"          desc:"etcd watch and retry configuration"`
	Targets       []Target `json:"targets"        desc:"certificate synchronization targets"`
}

// Etcd configures access to the remote etcd cluster.
type Etcd struct {
	Endpoints      []string      `json:"endpoints"       desc:"etcd endpoint URLs"`
	Username       string        `json:"username"        desc:"etcd username"`
	Password       string        `json:"password"        desc:"etcd password"`
	DialTimeout    time.Duration `json:"dial-timeout"    desc:"etcd connection timeout"`
	RequestTimeout time.Duration `json:"request-timeout" desc:"timeout for an etcd read"`
	TLS            EtcdTLS       `json:"tls"             desc:"etcd TLS client configuration"`
}

// EtcdTLS configures server verification and optional client authentication.
type EtcdTLS struct {
	CAFile             string `json:"ca-file"              desc:"PEM CA bundle used to verify etcd"`
	CertFile           string `json:"cert-file"            desc:"PEM client certificate used for etcd mTLS"`
	KeyFile            string `json:"key-file"             desc:"PEM client private key used for etcd mTLS"`
	ServerName         string `json:"server-name"          desc:"expected etcd TLS server name"`
	InsecureSkipVerify bool   `json:"insecure-skip-verify" desc:"skip etcd TLS server verification (unsafe)"`
}

// Watch configures active-pointer watches and safety resynchronization.
type Watch struct {
	RootPrefix     string        `json:"root-prefix"     desc:"root key prefix for the pemcast/v1 protocol"`
	ResyncInterval time.Duration `json:"resync-interval" desc:"periodic full reconciliation interval, zero disables it"`
	RetryMin       time.Duration `json:"retry-min"       desc:"minimum retry delay"`
	RetryMax       time.Duration `json:"retry-max"       desc:"maximum retry delay"`
	JitterRatio    float64       `json:"jitter-ratio"    desc:"random retry jitter ratio from zero to one"`
}

// Target maps one immutable remote bundle to a local certificate directory.
type Target struct {
	ID           string     `json:"id"            desc:"target identifier and remote active-pointer name"`
	DeletePolicy string     `json:"delete-policy" desc:"action when the active pointer is deleted: retain or fail"`
	Output       Output     `json:"output"        desc:"local release directory configuration"`
	Validation   Validation `json:"validation"    desc:"TLS material validation configuration"`
	Hook         Hook       `json:"hook"          desc:"post-activation hook configuration"`
}

// Output configures release-directory deployment.
type Output struct {
	Root           string        `json:"root"            desc:"local certificate root directory"`
	CurrentLink    string        `json:"current-link"    desc:"relative symlink selecting the active release"`
	RetainReleases int           `json:"retain-releases" desc:"number of inactive releases retained locally"`
	DirectoryMode  FileMode      `json:"directory-mode"  desc:"octal mode for managed directories"`
	Mappings       []FileMapping `json:"mappings"        desc:"remote bundle file to local file mappings"`
}

// FileMapping configures one file written from a remote bundle.
type FileMapping struct {
	Remote string   `json:"remote" desc:"file name in the remote bundle"`
	Local  string   `json:"local"  desc:"relative path inside each local release"`
	Mode   FileMode `json:"mode"   desc:"octal file mode"`
}

// Validation describes the certificate and key pair that must match.
type Validation struct {
	Certificate     string        `json:"certificate"    desc:"remote certificate file to validate"`
	PrivateKey      string        `json:"private-key"    desc:"remote private key file to validate"`
	RejectExpired   bool          `json:"reject-expired" desc:"reject an expired leaf certificate"`
	MinimumValidity time.Duration `json:"minimum-validity" desc:"minimum required remaining leaf validity"`
}

// Hook configures an executable invoked after a new release becomes active.
type Hook struct {
	Path    string        `json:"path"    desc:"post-activation executable path"`
	Args    []string      `json:"args"    desc:"post-activation executable arguments"`
	Timeout time.Duration `json:"timeout" desc:"hook execution timeout"`
}

// DefaultConfig returns safe operational defaults. A target is intentionally
// not supplied because local destinations are deployment-specific.
func DefaultConfig() Config {
	return Config{Agent: Agent{
		StateDir:      "/var/lib/pemcast",
		MaxConcurrent: 4,
		Etcd: Etcd{
			Endpoints:      []string{"http://127.0.0.1:2379"},
			DialTimeout:    5 * time.Second,
			RequestTimeout: 10 * time.Second,
		},
		Watch: Watch{
			RootPrefix:     "/pemcast/v1",
			ResyncInterval: 10 * time.Minute,
			RetryMin:       time.Second,
			RetryMax:       30 * time.Second,
			JitterRatio:    0.2,
		},
		Targets: []Target{{
			ID:           "nginx",
			DeletePolicy: "retain",
			Output: Output{
				Root:           "/etc/nginx/tls",
				CurrentLink:    "current",
				RetainReleases: 3,
				DirectoryMode:  FileMode("0700"),
				Mappings: []FileMapping{
					{Remote: "fullchain.pem", Local: "fullchain.pem", Mode: FileMode("0644")},
					{Remote: "privkey.pem", Local: "privkey.pem", Mode: FileMode("0600")},
				},
			},
			Validation: Validation{
				Certificate:     "fullchain.pem",
				PrivateKey:      "privkey.pem",
				RejectExpired:   true,
				MinimumValidity: time.Hour,
			},
			Hook: Hook{
				Path:    "/etc/pemcast/hooks/reload-nginx",
				Timeout: 30 * time.Second,
			},
		}},
	}}
}

// Validate checks cross-field constraints that cannot be expressed by cfgm's schema.
func (c Config) Validate() error {
	a := c.Agent
	if !filepath.IsAbs(a.StateDir) {
		return fmt.Errorf("agent.state-dir must be absolute")
	}
	if len(a.Etcd.Endpoints) == 0 {
		return fmt.Errorf("agent.etcd.endpoints is required")
	}
	for index, endpoint := range a.Etcd.Endpoints {
		if strings.TrimSpace(endpoint) == "" {
			return fmt.Errorf("agent.etcd.endpoints[%d] is empty", index)
		}
	}
	if a.Etcd.DialTimeout <= 0 || a.Etcd.RequestTimeout <= 0 {
		return fmt.Errorf("agent.etcd dial-timeout and request-timeout must be positive")
	}
	if (a.Etcd.TLS.CertFile == "") != (a.Etcd.TLS.KeyFile == "") {
		return fmt.Errorf("agent.etcd.tls cert-file and key-file must be configured together")
	}
	if strings.TrimSpace(a.Watch.RootPrefix) == "" ||
		!strings.HasPrefix(a.Watch.RootPrefix, "/") ||
		path.Clean(a.Watch.RootPrefix) != a.Watch.RootPrefix ||
		a.Watch.RootPrefix == "/" {
		return fmt.Errorf("agent.watch.root-prefix must be a clean, absolute, non-root key prefix")
	}
	if a.Watch.ResyncInterval < 0 || a.Watch.RetryMin <= 0 || a.Watch.RetryMax < a.Watch.RetryMin {
		return fmt.Errorf("agent.watch retry and resync durations are invalid")
	}
	if a.Watch.JitterRatio < 0 || a.Watch.JitterRatio >= 1 {
		return fmt.Errorf("agent.watch.jitter-ratio must be >= 0 and < 1")
	}
	if a.MaxConcurrent <= 0 {
		return fmt.Errorf("agent.max-concurrent must be positive")
	}
	if len(a.Targets) == 0 {
		return fmt.Errorf("agent.targets is required")
	}
	seen := make(map[string]struct{}, len(a.Targets))
	for i, target := range a.Targets {
		if err := target.Validate(); err != nil {
			return fmt.Errorf("agent.targets[%d]: %w", i, err)
		}
		if _, exists := seen[target.ID]; exists {
			return fmt.Errorf("agent target %q is configured more than once", target.ID)
		}
		seen[target.ID] = struct{}{}
	}
	return nil
}

// Manager owns configuration defaults, sources, CLI flags, and schema checks.
var Manager = cfgm.MustNew(
	DefaultConfig(),
	cfgm.AppName(AppName),
	cfgm.CLIAlias("agent.etcd.endpoints", "E"),
	cfgm.HideCLI("agent.etcd.password"),
)
