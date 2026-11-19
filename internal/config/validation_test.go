package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

func TestValidateTarget(t *testing.T) {
	cfg := singleSourceConfig(validTarget("nginx", filepath.Join(t.TempDir(), "tls")))
	require.NoError(t, cfg.Validate())
	cfg.Agent.Targets[0].Output.Mappings[1].Local = "../key.pem"
	require.ErrorContains(t, cfg.Validate(), "safe relative path")
}

func TestValidateRejectsUncleanRoot(t *testing.T) {
	cfg := singleSourceConfig(validTarget("nginx", filepath.Join(t.TempDir(), "tls")+string(filepath.Separator)))
	require.ErrorContains(t, cfg.Validate(), "clean, absolute, non-root path")
}

func TestValidateRejectsInvalidHookEnvironmentName(t *testing.T) {
	target := validTarget("nginx", filepath.Join(t.TempDir(), "tls"))
	target.Hook.PassEnvironment = []string{"BAD-NAME"}
	cfg := singleSourceConfig(target)
	require.ErrorContains(t, cfg.Validate(), "valid environment variable name")
}

func TestValidateTargetTypesAndRequiredMappings(t *testing.T) {
	target := validTarget("client", filepath.Join(t.TempDir(), "client"))
	target.Type = bundle.TypeTLSClient
	target.Validation.ServerNames = []string{"example.com"}
	cfg := singleSourceConfig(target)
	require.ErrorContains(t, cfg.Validate(), "server-names is only valid")

	target.Validation.ServerNames = nil
	cfg.Agent.Targets[0] = targetForSource("default", target)
	require.NoError(t, cfg.Validate())

	target.Type = bundle.TypeTrust
	target.Output.Mappings = []FileMapping{
		{Remote: bundle.NameCABundle, Local: "ca-bundle.pem", Mode: FileMode("0644")},
	}
	cfg.Agent.Targets[0] = targetForSource("default", target)
	require.NoError(t, cfg.Validate())

	target.Output.Mappings = []FileMapping{
		{Remote: "fullchain.pem", Local: "fullchain.pem", Mode: FileMode("0644")},
	}
	cfg.Agent.Targets[0] = targetForSource("default", target)
	require.ErrorContains(t, cfg.Validate(), "missing remote mapping")
}

func TestValidateRejectsOverlappingOutputRootsAcrossSources(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "tls")
	cfg := DefaultConfig()
	cfg.Agent.Etcds = []Source{source("one"), source("two")}
	cfg.Agent.Targets = []Target{
		targetForSource("one", validTarget("one", parent)),
		targetForSource("two", validTarget("two", filepath.Join(parent, "nested"))),
	}
	require.ErrorContains(t, cfg.Validate(), "overlap")

	cfg.Agent.Targets[1].Output.Root = parent + "-two"
	require.NoError(t, cfg.Validate())
}

func TestValidateAndBuildMultipleSourceRuntimes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agent.Etcds = []Source{
		validSourceEtcd("bendian", "/tenants/bendian", "bendian-agent:secret-one"),
		validSourceEtcd("lwmacct", "/tenants/lwmacct", "lwmacct-agent:secret-two"),
	}
	cfg.Agent.Targets = []Target{
		targetForSource("bendian", validTarget("shared", filepath.Join(t.TempDir(), "bendian"))),
		targetForSource("lwmacct", validTarget("shared", filepath.Join(t.TempDir(), "lwmacct"))),
	}

	require.NoError(t, cfg.Validate())
	runtimes, err := cfg.Agent.SourceRuntimes()
	require.NoError(t, err)
	require.Len(t, runtimes, 2)
	require.Equal(t, "bendian", runtimes[0].Name)
	require.Equal(t, "/tenants/bendian", runtimes[0].Etcd.Prefix)
	require.Equal(t, []string{"shared"}, targetIDs(runtimes[0].Targets))
	require.Equal(t, "lwmacct", runtimes[1].Name)
	require.Equal(t, []string{"shared"}, targetIDs(runtimes[1].Targets))
}

func TestSelectEtcdPrefersDefaultThenFirstAndMatchesName(t *testing.T) {
	cfg := DefaultConfig()
	first := validSourceEtcd("first", "/tenants/first", "first-agent:first-secret")
	defaultEtcd := validSourceEtcd("default", "/tenants/default", "default-agent:default-secret")
	second := validSourceEtcd("second", "/tenants/second", "second-agent:second-secret")
	cfg.Agent.Etcds = []Source{first, defaultEtcd, second}

	selected, err := cfg.Agent.SelectEtcd("")
	require.NoError(t, err)
	require.Equal(t, "default", selected.Name)

	cfg.Agent.Etcds = []Source{first, second}
	selected, err = cfg.Agent.SelectEtcd("")
	require.NoError(t, err)
	require.Equal(t, "first", selected.Name)

	selected, err = cfg.Agent.SelectEtcd("second")
	require.NoError(t, err)
	require.Equal(t, "second", selected.Name)

	_, err = cfg.Agent.SelectEtcd("missing")
	require.ErrorContains(t, err, "does not exist")
}

func TestManagementConfigUsesDedicatedCredential(t *testing.T) {
	source := validSourceEtcd("default", "/tenants/default", "agent:agent-secret")
	source.ManagementUser = "publisher:publisher-secret"

	agentConfig := source.ClientConfig()
	managementConfig := source.ManagementConfig()
	require.Equal(t, "agent:agent-secret", agentConfig.User)
	require.Equal(t, "publisher:publisher-secret", managementConfig.User)
	require.Equal(t, agentConfig.Prefix, managementConfig.Prefix)
	require.Equal(t, agentConfig.Endpoints, managementConfig.Endpoints)
}

func TestClientConfigAppliesEtcdTimeoutDefaults(t *testing.T) {
	source := validSourceEtcd("default", "/tenants/default", "")
	source.DialTimeout = 0
	source.RequestTimeout = 0

	etcd := source.ClientConfig()
	require.Equal(t, DefaultEtcdDialTimeout, etcd.DialTimeout)
	require.Equal(t, DefaultEtcdRequestTimeout, etcd.RequestTimeout)

	source.DialTimeout = -time.Second
	source.RequestTimeout = -time.Second
	etcd = source.ClientConfig()
	require.Equal(t, -time.Second, etcd.DialTimeout)
	require.Equal(t, -time.Second, etcd.RequestTimeout)
}

func TestValidateRejectsMissingOrUnusedSourceReferences(t *testing.T) {
	cfg := singleSourceConfig(validTarget("shared", filepath.Join(t.TempDir(), "tls")))
	cfg.Agent.Targets[0].Etcd = "missing"
	require.ErrorContains(t, cfg.Validate(), "does not exist")

	cfg = DefaultConfig()
	cfg.Agent.Etcds = []Source{source("bendian"), source("lwmacct")}
	cfg.Agent.Targets = []Target{targetForSource("bendian", validTarget("shared", filepath.Join(t.TempDir(), "tls")))}
	require.ErrorContains(t, cfg.Validate(), `agent etcd "lwmacct" has no targets`)

	cfg.Agent.Etcds = nil
	require.ErrorContains(t, cfg.Validate(), "agent.etcds is required")

	cfg.Agent.Etcds = []Source{source("bendian")}
	cfg.Agent.Targets = nil
	require.ErrorContains(t, cfg.Validate(), "agent.targets is required")
}

func TestValidateRejectsDuplicateTargetsSourcesAndInvalidUsers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tls")
	cfg := singleSourceConfig(
		validTarget("shared", root),
		validTarget("shared", root+"-two"),
	)
	require.ErrorContains(t, cfg.Validate(), "configured more than once")

	cfg = DefaultConfig()
	cfg.Agent.Etcds = []Source{
		validSourceEtcd("bendian", "/tenants/bendian", ""),
		validSourceEtcd("bendian", "/tenants/other", ""),
	}
	cfg.Agent.Targets = []Target{targetForSource("bendian", validTarget("shared", root))}
	require.ErrorContains(t, cfg.Validate(), "configured more than once")

	cfg = DefaultConfig()
	cfg.Agent.Etcds = []Source{validSourceEtcd("../unsafe", "/tenants/bendian", "")}
	cfg.Agent.Targets = []Target{targetForSource("../unsafe", validTarget("shared", root))}
	require.ErrorContains(t, cfg.Validate(), "safe etcd name")

	cfg = DefaultConfig()
	cfg.Agent.Etcds = []Source{validSourceEtcd("bendian", "/tenants/bendian", "missing-password")}
	cfg.Agent.Targets = []Target{targetForSource("bendian", validTarget("shared", root))}
	require.ErrorContains(t, cfg.Validate(), "etcd user")
}

func singleSourceConfig(targets ...Target) Config {
	cfg := DefaultConfig()
	cfg.Agent.Etcds = []Source{source("default")}
	for index := range targets {
		targets[index].Etcd = "default"
	}
	cfg.Agent.Targets = targets
	return cfg
}

func source(name string) Source {
	return validSourceEtcd(name, "/tenants/"+name, "")
}

func targetForSource(name string, target Target) Target {
	target.Etcd = name
	return target
}

func validTarget(id, root string) Target {
	return Target{
		ID: id, DeletePolicy: "retain", Type: bundle.TypeTLSServer,
		Output: Output{
			Root: root, CurrentLink: "current", RetainReleases: 1, DirectoryMode: FileMode("0700"),
			Mappings: []FileMapping{
				{Remote: bundle.NameCertificateChain, Local: "fullchain.pem", Mode: FileMode("0644")},
				{Remote: bundle.NamePrivateKey, Local: "privkey.pem", Mode: FileMode("0600")},
			},
		},
	}
}

func validSourceEtcd(name, prefix, user string) Source {
	return Source{
		Name: name, Prefix: prefix, Endpoints: []string{"https://etcd.example:2379"}, User: user,
		DialTimeout: time.Second, RequestTimeout: time.Second,
	}
}

func targetIDs(targets []Target) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.ID)
	}
	return ids
}
