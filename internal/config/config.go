// Package config provides pemcast application configuration.
package config

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lwmacct/251207-go-pkg-cfgm/pkg/cfgm"
	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
)

const AppName = "pemcast"

// DefaultEtcdPrefix is the default namespace placed before the fixed v6 protocol root.
const DefaultEtcdPrefix = "/pemcast"

// Config is grouped by CLI subcommand so cfgm can trim command prefixes.
type Config struct {
	Agent Agent `json:"agent" desc:"pemcast agent configuration"`
	Tools Tools `json:"tools" desc:"pemcast local command tools"`
}

// Agent configures the long-running or one-shot synchronization agent.
type Agent struct {
	Once          bool     `json:"once"           desc:"synchronize once and exit"`
	DryRun        bool     `json:"dry-run"        desc:"validate changes without writing files or running hooks"`
	MaxConcurrent int      `json:"max-concurrent" desc:"maximum targets reconciled concurrently"`
	Etcds         []Source `json:"etcds"           desc:"named etcd clusters for a single-process multi-tenant agent"`
	Watch         Watch    `json:"watch"          desc:"etcd watch and retry configuration"`
	Targets       []Target `json:"targets"        desc:"certificate synchronization targets"`
}

// Tools groups command-specific local tool configuration.
type Tools struct {
	Pack Pack `json:"pack" desc:"local v6 pack builder configuration"`
	Seed Seed `json:"seed" desc:"local v6 release seeder configuration"`
}

// Pack configures the offline deterministic v6 pack builder.
type Pack struct {
	Type            string `json:"type"        desc:"certificate bundle type: tls-server, tls-client, or trust"`
	TargetID        string `json:"target"      desc:"target ID"`
	CertificatePath string `json:"certificate" desc:"path to fullchain.pem"`
	PrivateKeyPath  string `json:"private-key" desc:"path to privkey.pem"`
	CAPath          string `json:"ca"          desc:"path to ca-bundle.pem for trust"`
	EtcdPrefix      string `json:"etcd-prefix" desc:"etcd namespace prefix placed before /v6"`
	OutputDir       string `json:"output-dir"  desc:"new absolute directory for bundle.json, metadata.json, and stage.txn"`
}

// Seed configures offline materialization of one validated v6 pack.
type Seed struct {
	TargetID string `json:"target"   desc:"target ID from the agent configuration"`
	Etcd     string `json:"etcd"     desc:"agent.etcds name when multiple targets share an ID"`
	PackDir  string `json:"pack-dir" desc:"directory containing bundle.json and metadata.json"`
	Force    bool   `json:"force"    desc:"replace an existing different local release during an explicit rescue"`
}

// Etcd configures access to the remote etcd cluster.
type Etcd struct {
	Prefix         string        `json:"prefix"          desc:"etcd namespace prefix placed before /v6"`
	Endpoints      []string      `json:"endpoints"       desc:"etcd endpoint URLs"`
	User           string        `json:"user"            desc:"etcd authentication as username:password; empty disables authentication"`
	DialTimeout    time.Duration `json:"dial-timeout"    desc:"etcd connection timeout"`
	RequestTimeout time.Duration `json:"request-timeout" desc:"timeout for an etcd read"`
	TLS            EtcdTLS       `json:"tls"             desc:"etcd TLS client configuration"`
}

// ManagementConfig returns the same connection settings with the optional
// management credential substituted for the agent credential.
func (s Source) ManagementConfig() Etcd {
	cfg := s.ClientConfig()
	cfg.User = s.ManagementUser
	return cfg
}

// Source names one etcd configuration so targets can select their remote
// tenant without making the source name part of the remote protocol keys.
type Source struct {
	Name           string        `json:"name"            desc:"unique agent-local etcd name"`
	Prefix         string        `json:"prefix"          desc:"etcd namespace prefix placed before /v6"`
	Endpoints      []string      `json:"endpoints"       desc:"etcd endpoint URLs"`
	User           string        `json:"user"            desc:"etcd authentication as username:password; empty disables authentication"`
	DialTimeout    time.Duration `json:"dial-timeout"    desc:"etcd connection timeout"`
	RequestTimeout time.Duration `json:"request-timeout" desc:"timeout for an etcd read"`
	TLS            EtcdTLS       `json:"tls"             desc:"etcd TLS client configuration"`
	ManagementUser string        `json:"management-user"  desc:"optional username:password for management commands; environment fallback is used when empty"`
}

// ClientConfig returns the command-agnostic etcd client fields.
func (s Source) ClientConfig() Etcd {
	return Etcd{
		Prefix: s.Prefix, Endpoints: s.Endpoints, User: s.User,
		DialTimeout: s.DialTimeout, RequestTimeout: s.RequestTimeout, TLS: s.TLS,
	}
}

// EtcdTLS configures server verification and optional client authentication.
type EtcdTLS struct {
	CAFile             string `json:"ca-file"              desc:"PEM CA bundle used to verify etcd"`
	CertFile           string `json:"cert-file"            desc:"PEM client certificate used for etcd mTLS"`
	KeyFile            string `json:"key-file"             desc:"PEM client private key used for etcd mTLS"`
	ServerName         string `json:"server-name"          desc:"expected etcd TLS server name"`
	InsecureSkipVerify bool   `json:"insecure-skip-verify" desc:"skip etcd TLS server verification (unsafe)"`
}

// Watch configures active-pointer watch liveness and recovery.
type Watch struct {
	ProgressInterval time.Duration `json:"progress-interval" desc:"interval for requesting etcd watch progress"`
	ProgressTimeout  time.Duration `json:"progress-timeout"  desc:"maximum time without a watch event or progress response"`
	RetryMin         time.Duration `json:"retry-min"          desc:"minimum snapshot/watch restart delay"`
	RetryMax         time.Duration `json:"retry-max"          desc:"maximum snapshot/watch restart delay"`
	JitterRatio      float64       `json:"jitter-ratio"       desc:"random restart jitter ratio from zero to one"`
}

// Target maps one immutable remote bundle to a local certificate directory.
type Target struct {
	ID           string     `json:"id"            desc:"target identifier and remote active-pointer name"`
	Etcd         string     `json:"etcd"          desc:"agent.etcds name owning this target"`
	Type         string     `json:"type"          desc:"certificate bundle type: tls-server, tls-client, or trust"`
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

// Validation describes target-local certificate policy.
type Validation struct {
	RejectExpired   bool          `json:"reject-expired" desc:"reject expired bundle certificates"`
	MinimumValidity time.Duration `json:"minimum-validity" desc:"minimum required remaining certificate validity"`
	ServerNames     []string      `json:"server-names"    desc:"required DNS names or IP addresses for tls-server targets"`
}

// Hook configures an executable invoked after a new release becomes active.
type Hook struct {
	Path            string        `json:"path"              desc:"post-activation executable path"`
	Args            []string      `json:"args"              desc:"post-activation executable arguments"`
	Timeout         time.Duration `json:"timeout"           desc:"hook execution timeout"`
	PassEnvironment []string      `json:"pass-environment"  desc:"existing environment variable names explicitly passed to the hook"`
}

// DefaultConfig returns safe operational defaults. No target is supplied
// because local destinations and reload hooks are deployment-specific.
func DefaultConfig() Config {
	return Config{
		Agent: Agent{
			MaxConcurrent: 4,
			Watch: Watch{
				ProgressInterval: 30 * time.Second,
				ProgressTimeout:  2 * time.Minute,
				RetryMin:         time.Second,
				RetryMax:         30 * time.Second,
				JitterRatio:      0.2,
			},
		},
		Tools: Tools{Pack: Pack{Type: bundle.TypeTLSServer, EtcdPrefix: DefaultEtcdPrefix}},
	}
}

// ExampleConfig returns defaults plus one representative target for operators
// copying the generated example into a deployment-specific configuration.
func ExampleConfig() Config {
	cfg := DefaultConfig()
	cfg.Agent.Etcds = []Source{
		{
			Name:           "default",
			Prefix:         DefaultEtcdPrefix,
			Endpoints:      []string{"http://127.0.0.1:2379"},
			DialTimeout:    5 * time.Second,
			RequestTimeout: 10 * time.Second,
		},
	}
	cfg.Agent.Targets = []Target{
		{
			ID:           "nginx",
			Etcd:         "default",
			Type:         bundle.TypeTLSServer,
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
				RejectExpired:   true,
				MinimumValidity: time.Hour,
			},
			Hook: Hook{
				Path: "/etc/pemcast/hooks/reload-nginx", Timeout: 30 * time.Second,
			},
		},
	}
	return cfg
}

// Validate checks cross-field constraints that cannot be expressed by cfgm's schema.
func (c Config) Validate() error {
	return c.Agent.Validate()
}

// Validate checks the complete agent source and target graph.
func (a Agent) Validate() error {
	if a.Watch.ProgressInterval <= 0 || a.Watch.ProgressTimeout < 3*a.Watch.ProgressInterval {
		return fmt.Errorf("agent.watch progress interval and timeout are invalid")
	}
	if a.Watch.RetryMin <= 0 || a.Watch.RetryMax < a.Watch.RetryMin {
		return fmt.Errorf("agent.watch retry durations are invalid")
	}
	if a.Watch.JitterRatio < 0 || a.Watch.JitterRatio >= 1 {
		return fmt.Errorf("agent.watch.jitter-ratio must be >= 0 and < 1")
	}
	if a.MaxConcurrent <= 0 {
		return fmt.Errorf("agent.max-concurrent must be positive")
	}
	sourceNames, err := a.validateEtcds()
	if err != nil {
		return err
	}

	if len(a.Targets) == 0 {
		return fmt.Errorf("agent.targets is required")
	}
	seen := make(map[string]struct{}, len(a.Targets))
	usedSources := make(map[string]struct{}, len(a.Etcds))
	roots := make([]string, 0, len(a.Targets))
	for index, target := range a.Targets {
		if err := target.Validate(); err != nil {
			return fmt.Errorf("agent.targets[%d]: %w", index, err)
		}
		if _, exists := sourceNames[target.Etcd]; !exists {
			return fmt.Errorf("agent.targets[%d].etcd %q does not exist", index, target.Etcd)
		}
		targetKey := target.Etcd + "\x00" + target.ID
		if _, exists := seen[targetKey]; exists {
			return fmt.Errorf("agent etcd %q target %q is configured more than once", target.Etcd, target.ID)
		}
		seen[targetKey] = struct{}{}
		usedSources[target.Etcd] = struct{}{}
		roots = append(roots, target.Output.Root)
	}
	for _, source := range a.Etcds {
		if _, used := usedSources[source.Name]; !used {
			return fmt.Errorf("agent etcd %q has no targets", source.Name)
		}
	}
	return validateOutputRoots(roots)
}

func validateEtcd(cfg Etcd, fieldPrefix string) error {
	if len(cfg.Endpoints) == 0 {
		return fmt.Errorf("%s.endpoints is required", fieldPrefix)
	}
	if _, err := keyspace.NewKeys(cfg.Prefix); err != nil {
		return err
	}
	for index, endpoint := range cfg.Endpoints {
		if strings.TrimSpace(endpoint) == "" {
			return fmt.Errorf("%s.endpoints[%d] is empty", fieldPrefix, index)
		}
	}
	if _, _, err := SplitEtcdUser(cfg.User); err != nil {
		return err
	}
	if cfg.DialTimeout <= 0 || cfg.RequestTimeout <= 0 {
		return fmt.Errorf("%s dial-timeout and request-timeout must be positive", fieldPrefix)
	}
	if (cfg.TLS.CertFile == "") != (cfg.TLS.KeyFile == "") {
		return fmt.Errorf("%s.tls cert-file and key-file must be configured together", fieldPrefix)
	}
	return nil
}

func (a Agent) validateEtcds() (map[string]struct{}, error) {
	if len(a.Etcds) == 0 {
		return nil, fmt.Errorf("agent.etcds is required")
	}
	names := make(map[string]struct{}, len(a.Etcds))
	for index, source := range a.Etcds {
		if !safeBundleName(source.Name) {
			return nil, fmt.Errorf("agent.etcds[%d].name must be a safe etcd name", index)
		}
		for prior := 0; prior < index; prior++ {
			if a.Etcds[prior].Name == source.Name {
				return nil, fmt.Errorf("agent etcd %q is configured more than once", source.Name)
			}
		}
		if err := validateEtcd(source.ClientConfig(), fmt.Sprintf("agent.etcds[%d]", index)); err != nil {
			return nil, fmt.Errorf("agent.etcds[%d]: %w", index, err)
		}
		if _, _, err := SplitEtcdUser(source.ManagementUser); err != nil {
			return nil, fmt.Errorf("agent.etcds[%d].management-user: %w", index, err)
		}
		names[source.Name] = struct{}{}
	}
	return names, nil
}

// SelectEtcd returns one configured etcd. An empty name prefers the entry
// named default and otherwise selects the first configured entry.
func (a Agent) SelectEtcd(name string) (Source, error) {
	if _, err := a.validateEtcds(); err != nil {
		return Source{}, err
	}
	if name == "" {
		for _, source := range a.Etcds {
			if source.Name == "default" {
				return source, nil
			}
		}
		return a.Etcds[0], nil
	}
	for _, source := range a.Etcds {
		if source.Name == name {
			return source, nil
		}
	}
	return Source{}, fmt.Errorf("agent etcd %q does not exist", name)
}

// SplitEtcdUser splits the etcdctl-style `username:password` value at the
// first colon. Everything after that colon belongs to the password.
func SplitEtcdUser(value string) (string, string, error) {
	if value == "" {
		return "", "", nil
	}
	username, password, found := strings.Cut(value, ":")
	if !found || username == "" || password == "" {
		return "", "", fmt.Errorf("etcd user must be empty or formatted as username:password")
	}
	return username, password, nil
}

// SourceRuntime is one validated source and the targets that belong to it.
type SourceRuntime struct {
	Name    string
	Etcd    Etcd
	Targets []Target
}

// SourceRuntimes returns one runtime entry per configured etcd source.
// Validate should run first; this method reports impossible structures only.
func (a Agent) SourceRuntimes() ([]SourceRuntime, error) {
	if len(a.Etcds) == 0 {
		return nil, fmt.Errorf("agent.etcds is required")
	}
	runtimes := make([]SourceRuntime, 0, len(a.Etcds))
	byName := make(map[string]int, len(a.Etcds))
	for _, source := range a.Etcds {
		byName[source.Name] = len(runtimes)
		runtimes = append(runtimes, SourceRuntime{
			Name: source.Name, Etcd: source.ClientConfig(),
		})
	}
	for _, target := range a.Targets {
		index := byName[target.Etcd]
		runtimes[index].Targets = append(runtimes[index].Targets, target)
	}
	return runtimes, nil
}

// AllTargets returns every configured target.
func (a Agent) AllTargets() []Target {
	return append([]Target(nil), a.Targets...)
}

func validateOutputRoots(roots []string) error {
	for i, left := range roots {
		for _, right := range roots[i+1:] {
			relation, err := filepath.Rel(left, right)
			if err != nil {
				return fmt.Errorf("compare output roots %q and %q: %w", left, right, err)
			}
			if relation == "." || !strings.HasPrefix(relation, "..") {
				return fmt.Errorf("output roots %q and %q overlap", left, right)
			}
		}
	}
	return nil
}

// Manager owns configuration defaults, sources, CLI flags, and schema checks.
var Manager = cfgm.MustNew(
	DefaultConfig(),
	cfgm.AppName(AppName),
)

// PublishEtcdUserTemplate is the publish-specific credential fallback.
const PublishEtcdUserTemplate = `${ETCDCTL_USER_PUBLISH:-${ETCDCTL_USER:-}}`

// UpgradeEtcdUserTemplate prefers a dedicated migration credential before the
// broad publisher and generic etcdctl fallbacks.
const UpgradeEtcdUserTemplate = `${ETCDCTL_USER_UPGRADE:-${ETCDCTL_USER_PUBLISH:-${ETCDCTL_USER:-}}}`

// PruneEtcdUserTemplate prefers a dedicated lifecycle credential before the
// broad publisher and generic etcdctl fallbacks.
const PruneEtcdUserTemplate = `${ETCDCTL_USER_PRUNE:-${ETCDCTL_USER_PUBLISH:-${ETCDCTL_USER:-}}}`

// LoadCommand loads the normal file and environment configuration sources.
func LoadCommand(ctx context.Context, root *cli.Command) (*Config, error) {
	sources := make([]cfgm.Source, 0, 2)
	if root != nil {
		if path := strings.TrimSpace(root.String("config")); path != "" {
			sources = append(sources, cfgm.File(path))
		}
	}
	prefix := "PEMCAST_"
	if root != nil && root.IsSet("env-prefix") {
		prefix = root.String("env-prefix")
	}
	if prefix != "" {
		sources = append(sources, cfgm.Env(prefix))
	}
	return Manager.Load(ctx, sources...)
}

// ResolveEtcdCommand loads configuration, selects one agent etcd, and resolves
// its command-specific management credential only when no file credential is set.
func ResolveEtcdCommand(
	ctx context.Context, root *cli.Command, name, etcdUserTemplate string,
) (Etcd, error) {
	cfg, err := LoadCommand(ctx, root)
	if err != nil {
		return Etcd{}, err
	}
	source, err := cfg.Agent.SelectEtcd(name)
	if err != nil {
		return Etcd{}, err
	}
	etcd := source.ManagementConfig()
	if etcd.User == "" {
		etcd.User, err = ExpandEtcdUser(ctx, etcdUserTemplate)
	}
	return etcd, err
}

type etcdUserConfig struct {
	User string `json:"user"`
}

// ExpandEtcdUser uses cfgm template expansion to resolve command-specific
// ETCDCTL_USER fallbacks. An empty result disables etcd authentication.
func ExpandEtcdUser(ctx context.Context, templateValue string) (string, error) {
	manager, err := cfgm.New(
		etcdUserConfig{User: templateValue},
		cfgm.WithoutDefaultPaths(),
	)
	if err != nil {
		return "", fmt.Errorf("create etcd user config manager: %w", err)
	}
	credentials, err := manager.Load(ctx)
	if err != nil {
		return "", fmt.Errorf("expand etcd user template: %w", err)
	}
	return credentials.User, nil
}
