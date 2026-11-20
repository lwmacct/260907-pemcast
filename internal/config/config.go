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

// cfgm expands these standard etcdctl-compatible defaults before the config is
// decoded. Command-specific credential aliases are deliberately not used.
const (
	DefaultEtcdEndpointsTemplate = `${ETCDCTL_ENDPOINTS:-}`
	DefaultEtcdUserTemplate      = `${ETCDCTL_USER:-}`
)

// DefaultEtcdPrefix is the default namespace placed before the fixed v6 protocol root.
const DefaultEtcdPrefix = "/pemcast"

const (
	DefaultEtcdDialTimeout    = 5 * time.Second
	DefaultEtcdRequestTimeout = 10 * time.Second
)

// Fixed local deployment values are intentionally not target configuration.
// Consumers mount the output root; `current` and role aliases are public
// implementation details of every managed root.
const (
	DefaultCurrentLink    = "current"
	DefaultDirectoryMode  = FileMode("0700")
	DefaultRetainReleases = 3
)

const (
	DefaultDeletePolicy    = "retain"
	DefaultMinimumValidity = 336 * time.Hour
)

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

// Source names one etcd configuration so targets can select their remote
// tenant without making the source name part of the remote protocol keys.
type Source struct {
	Name           string        `json:"name"            desc:"unique agent-local etcd name"`
	Prefix         string        `json:"prefix"          desc:"etcd namespace prefix placed before /v6"`
	Endpoints      string        `json:"endpoints"       desc:"comma-separated etcd endpoint URLs"`
	User           string        `json:"user"            desc:"agent authentication as username:password; empty disables authentication"`
	DialTimeout    time.Duration `json:"dial-timeout"    desc:"etcd connection timeout"`
	RequestTimeout time.Duration `json:"request-timeout" desc:"timeout for an etcd read"`
	TLS            EtcdTLS       `json:"tls"             desc:"etcd TLS client configuration"`
	ManagementUser string        `json:"management-user"  desc:"management authentication as username:password; empty disables authentication"`
}

// ClientConfig returns the command-agnostic etcd client fields.
func (s Source) ClientConfig() Etcd {
	dialTimeout := s.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = DefaultEtcdDialTimeout
	}
	requestTimeout := s.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = DefaultEtcdRequestTimeout
	}
	return Etcd{
		Prefix: s.Prefix, Endpoints: SplitEtcdEndpoints(s.Endpoints), User: s.User,
		DialTimeout: dialTimeout, RequestTimeout: requestTimeout, TLS: s.TLS,
	}
}

// ManagementConfig returns the same connection settings with the management
// credential substituted for the agent credential.
func (s Source) ManagementConfig() Etcd {
	cfg := s.ClientConfig()
	cfg.User = s.ManagementUser
	return cfg
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
	Root  string                `json:"root"           desc:"local certificate root directory"`
	Files map[string]OutputFile `json:"files"                desc:"role-to-public-file overrides"`
}

// OutputFile configures the public name and mode for one bundle role.
type OutputFile struct {
	Name string   `json:"name" desc:"public file name in the output root"`
	Mode FileMode `json:"mode" desc:"octal file mode"`
}

// OrderedOutputFile is the canonical runtime view of one configured role.
type OrderedOutputFile struct {
	Role   string
	Remote string
	Name   string
	Mode   FileMode
}

// OrderedFiles returns output files in notification-safe order: private key
// first, then certificate chain. Trust bundles have a single role.
func (o Output) OrderedFiles() []OrderedOutputFile {
	order := []struct {
		role   string
		remote string
	}{
		{role: "private-key", remote: bundle.NamePrivateKey},
		{role: "certificate-chain", remote: bundle.NameCertificateChain},
		{role: "ca-bundle", remote: bundle.NameCABundle},
	}
	files := make([]OrderedOutputFile, 0, len(o.Files))
	for _, item := range order {
		if file, exists := o.Files[item.role]; exists {
			files = append(files, OrderedOutputFile{
				Role: item.role, Remote: item.remote, Name: file.Name, Mode: file.Mode,
			})
		}
	}
	return files
}

// Validation describes target-local certificate policy.
type Validation struct {
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
			Etcds: []Source{
				{
					Name:           "default",
					Prefix:         DefaultEtcdPrefix,
					Endpoints:      DefaultEtcdEndpointsTemplate,
					User:           DefaultEtcdUserTemplate,
					DialTimeout:    DefaultEtcdDialTimeout,
					RequestTimeout: DefaultEtcdRequestTimeout,
					ManagementUser: DefaultEtcdUserTemplate,
				},
			},
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
	cfg.Agent.Targets = []Target{
		{
			ID:           "nginx",
			Etcd:         "default",
			Type:         bundle.TypeTLSServer,
			DeletePolicy: DefaultDeletePolicy,
			Output: Output{
				Root: "/etc/nginx/tls",
				Files: map[string]OutputFile{
					"certificate-chain": {Name: "tls.crt", Mode: FileMode("0644")},
				},
			},
			Validation: Validation{
				MinimumValidity: DefaultMinimumValidity,
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
	return c.Normalized().Agent.Validate()
}

// Normalized returns a copied config with target-specific output-file
// defaults resolved. Unknown target types are left untouched for validation.
func (c Config) Normalized() Config {
	c.Agent.Targets = append([]Target(nil), c.Agent.Targets...)
	for index, target := range c.Agent.Targets {
		c.Agent.Targets[index] = target.Normalized()
	}
	return c
}

// Normalized resolves output-file defaults for one target.
func (t Target) Normalized() Target {
	if t.DeletePolicy == "" {
		t.DeletePolicy = DefaultDeletePolicy
	}
	if t.Validation.MinimumValidity == 0 {
		t.Validation.MinimumValidity = DefaultMinimumValidity
	}
	t.Output.Files = normalizeOutputFiles(t.Type, t.Output.Files)
	return t
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

// SelectEtcd returns the one configured etcd selected by its explicit name.
func (a Agent) SelectEtcd(name string) (Source, error) {
	if _, err := a.validateEtcds(); err != nil {
		return Source{}, err
	}
	if name == "" {
		return Source{}, fmt.Errorf("agent etcd name is required")
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

// SplitEtcdEndpoints converts the etcdctl-style comma-separated endpoint list
// into clientv3 endpoints. Empty elements remain present for validation.
func SplitEtcdEndpoints(value string) []string {
	if value == "" {
		return nil
	}
	endpoints := strings.Split(value, ",")
	for index := range endpoints {
		endpoints[index] = strings.TrimSpace(endpoints[index])
	}
	return endpoints
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

// ManagerAction wraps cfgm's typed CLI action so every command receives the
// same normalized target output roles as LoadCommand.
func ManagerAction(run func(context.Context, *cli.Command, *Config) error) cli.ActionFunc {
	return Manager.Action(func(ctx context.Context, command *cli.Command, cfg *Config) error {
		normalized := cfg.Normalized()
		return run(ctx, command, &normalized)
	})
}

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
	cfg, err := Manager.Load(ctx, sources...)
	if err != nil {
		return nil, err
	}
	normalized := cfg.Normalized()
	return &normalized, nil
}

// ResolveEtcdCommand loads configuration and selects one source by its
// explicit name for a management command.
func ResolveEtcdCommand(ctx context.Context, root *cli.Command, name string) (Etcd, error) {
	cfg, err := LoadCommand(ctx, root)
	if err != nil {
		return Etcd{}, err
	}
	source, err := cfg.Agent.SelectEtcd(name)
	if err != nil {
		return Etcd{}, err
	}
	return source.ManagementConfig(), nil
}
