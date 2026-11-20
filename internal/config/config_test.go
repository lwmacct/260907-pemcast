package config

import (
	"os"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/251207-go-pkg-cfgm/pkg/cfgm"
)

var files = cfgm.ConfigFiles[Config]{
	Manager: Manager, ExampleFile: "config/config.example.yaml", RuntimeFile: "config/config.yaml",
}

func TestWriteConfigExample(t *testing.T) {
	data, err := cfgm.ExampleYAML(ExampleConfig())
	if err != nil {
		t.Fatalf("generate config example: %v", err)
	}
	if err := os.WriteFile("../../config/config.example.yaml", data, 0600); err != nil {
		t.Fatalf("write config example: %v", err)
	}
}

func TestRuntimeConfigKeysValid(t *testing.T) { files.ValidateRuntimeConfig(t) }

func TestDefaultConfigHasOneEtcdAndNoTargets(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Agent.Etcds) != 1 || cfg.Agent.Etcds[0].Name != "default" {
		t.Fatalf("DefaultConfig().Agent.Etcds = %#v, want one default source", cfg.Agent.Etcds)
	}
	if got := cfg.Agent.Targets; len(got) != 0 {
		t.Fatalf("DefaultConfig().Agent.Targets = %#v, want none", got)
	}
}

func TestSplitEtcdUserUsesFirstColon(t *testing.T) {
	username, password, err := SplitEtcdUser("agent:secret:with:colons")
	if err != nil {
		t.Fatalf("SplitEtcdUser() error = %v", err)
	}
	if username != "agent" || password != "secret:with:colons" {
		t.Fatalf("SplitEtcdUser() = %q, %q", username, password)
	}

	if _, _, err := SplitEtcdUser(""); err != nil {
		t.Fatalf("empty user must disable authentication, got %v", err)
	}
	for _, value := range []string{"agent", ":secret", "agent:", ":", "agent:secret"} {
		if value == "agent:secret" {
			continue
		}
		if _, _, err := SplitEtcdUser(value); err == nil {
			t.Fatalf("SplitEtcdUser(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSplitEtcdEndpoints(t *testing.T) {
	if got := SplitEtcdEndpoints(""); got != nil {
		t.Fatalf("SplitEtcdEndpoints(\"\") = %#v, want nil", got)
	}
	got := SplitEtcdEndpoints(" http://a:2379 , http://b:2379 ")
	want := []string{"http://a:2379", "http://b:2379"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("SplitEtcdEndpoints() = %#v, want %#v", got, want)
		}
	}
	got = SplitEtcdEndpoints("http://a:2379,,http://b:2379")
	if len(got) != 3 || got[1] != "" {
		t.Fatalf("SplitEtcdEndpoints() kept an invalid list incorrectly: %#v", got)
	}
}

func TestExampleConfigHasRepresentativeTarget(t *testing.T) {
	got := ExampleConfig().Agent.AllTargets()
	if len(got) != 1 || got[0].ID != "nginx" {
		t.Fatalf("ExampleConfig() targets = %#v", got)
	}
}

func TestCfgmExpandsStandardEtcdctlDefaults(t *testing.T) {
	t.Setenv("ETCDCTL_ENDPOINTS", "http://etcd-a:2379,http://etcd-b:2379")
	t.Setenv("ETCDCTL_USER", "agent:agent-secret")

	manager := cfgm.MustNew(DefaultConfig(), cfgm.AppName(AppName), cfgm.WithoutDefaultPaths())
	cfg, err := manager.Load(t.Context())
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	source := cfg.Agent.Etcds[0]
	if source.User != "agent:agent-secret" || source.ManagementUser != "agent:agent-secret" {
		t.Fatalf("expanded users = %q, %q", source.User, source.ManagementUser)
	}
	endpoints := source.ClientConfig().Endpoints
	if len(endpoints) != 2 || endpoints[0] != "http://etcd-a:2379" || endpoints[1] != "http://etcd-b:2379" {
		t.Fatalf("expanded endpoints = %#v", endpoints)
	}
}

func TestResolveEtcdCommandRequiresExplicitSourceAndUsesManagementUser(t *testing.T) {
	t.Setenv("PEMCAST_AGENT_ETCDS", `[{"name":"default","prefix":"/tenants/default","endpoints":"http://etcd-a:2379,http://etcd-b:2379","user":"agent:agent-secret","management-user":"publisher:publisher-secret","dial-timeout":"1s","request-timeout":"1s"}]`)
	root := &cli.Command{Name: "pemcast"}
	Manager.MustConfigure(root)

	if _, err := ResolveEtcdCommand(t.Context(), root, ""); err == nil {
		t.Fatal("ResolveEtcdCommand() unexpectedly accepted an empty source name")
	}
	if _, err := ResolveEtcdCommand(t.Context(), root, "missing"); err == nil {
		t.Fatal("ResolveEtcdCommand() unexpectedly accepted a missing source")
	}

	etcd, err := ResolveEtcdCommand(t.Context(), root, "default")
	if err != nil {
		t.Fatalf("ResolveEtcdCommand() error = %v", err)
	}
	if etcd.User != "publisher:publisher-secret" {
		t.Fatalf("ResolveEtcdCommand() user = %q", etcd.User)
	}
	if len(etcd.Endpoints) != 2 || etcd.Endpoints[1] != "http://etcd-b:2379" {
		t.Fatalf("ResolveEtcdCommand() endpoints = %#v", etcd.Endpoints)
	}
}
