package config

import (
	"os"
	"testing"

	"github.com/lwmacct/251207-go-pkg-cfgm/pkg/cfgm"
)

var files = cfgm.ConfigFiles[Config]{
	Manager: Manager, ExampleFile: "config/config.example.yaml", RuntimeFile: "config/config.yaml",
}

func TestWriteConfigExample(t *testing.T) {
	data, err := cfgm.ExampleYAML(ExampleConfig())
	if err != nil {
		t.Fatalf("generate example config: %v", err)
	}
	if err := os.WriteFile("../../config/config.example.yaml", data, 0600); err != nil {
		t.Fatalf("write example config: %v", err)
	}
}
func TestRuntimeConfigKeysValid(t *testing.T) { files.ValidateRuntimeConfig(t) }

func TestDefaultConfigHasNoTargets(t *testing.T) {
	if got := DefaultConfig().Agent.Targets; len(got) != 0 {
		t.Fatalf("DefaultConfig() targets = %#v, want none", got)
	}
}

func TestDefaultConfigUsesDefaultEtcdPrefix(t *testing.T) {
	if got := DefaultConfig().Agent.Etcd.Prefix; got != DefaultEtcdPrefix {
		t.Fatalf("default etcd prefix = %q, want %q", got, DefaultEtcdPrefix)
	}
}

func TestValidateCommonAcceptsCustomEtcdPrefix(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agent.Etcd.Prefix = "/tenants/example"

	if err := cfg.Agent.ValidateCommon(); err != nil {
		t.Fatalf("ValidateCommon() error = %v", err)
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

func TestEtcdUserTemplateFallback(t *testing.T) {
	t.Setenv("ETCDCTL_USER", "fallback:fallback-secret")
	t.Setenv("ETCDCTL_USER_PUBLISH", "publish:publish-secret")

	user, err := ExpandEtcdUser(t.Context(), PublishEtcdUserTemplate)
	if err != nil {
		t.Fatalf("load environment credentials: %v", err)
	}
	if got := user; got != "publish:publish-secret" {
		t.Fatalf("specific etcd user alias = %q", got)
	}

	if err := os.Unsetenv("ETCDCTL_USER_PUBLISH"); err != nil {
		t.Fatalf("unset specific alias: %v", err)
	}
	user, err = ExpandEtcdUser(t.Context(), PublishEtcdUserTemplate)
	if err != nil {
		t.Fatalf("load alias credentials: %v", err)
	}
	if got := user; got != "fallback:fallback-secret" {
		t.Fatalf("generic etcd user fallback = %q", got)
	}

	if err := os.Unsetenv("ETCDCTL_USER"); err != nil {
		t.Fatalf("unset fallback alias: %v", err)
	}
	user, err = ExpandEtcdUser(t.Context(), PublishEtcdUserTemplate)
	if err != nil {
		t.Fatalf("load without aliases: %v", err)
	}
	if got := user; got != "" {
		t.Fatalf("empty credential fallback = %q, want empty", got)
	}
}

func TestUpgradeEtcdUserTemplateFallback(t *testing.T) {
	t.Setenv("ETCDCTL_USER", "fallback:fallback-secret")
	t.Setenv("ETCDCTL_USER_PUBLISH", "publish:publish-secret")
	t.Setenv("ETCDCTL_USER_UPGRADE", "upgrade:upgrade-secret")

	user, err := ExpandEtcdUser(t.Context(), UpgradeEtcdUserTemplate)
	if err != nil {
		t.Fatalf("load dedicated upgrade credential: %v", err)
	}
	if got := user; got != "upgrade:upgrade-secret" {
		t.Fatalf("UpgradeEtcdUserTemplate() = %q, want dedicated upgrade credential", got)
	}

	if err := os.Unsetenv("ETCDCTL_USER_UPGRADE"); err != nil {
		t.Fatalf("unset dedicated upgrade credential: %v", err)
	}
	user, err = ExpandEtcdUser(t.Context(), UpgradeEtcdUserTemplate)
	if err != nil {
		t.Fatalf("load publisher fallback: %v", err)
	}
	if got := user; got != "publish:publish-secret" {
		t.Fatalf("UpgradeEtcdUserTemplate() = %q, want publisher fallback", got)
	}
}

func TestExampleConfigHasRepresentativeTarget(t *testing.T) {
	got := ExampleConfig().Agent.Targets
	if len(got) != 1 || got[0].ID != "nginx" {
		t.Fatalf("ExampleConfig() targets = %#v, want one nginx target", got)
	}
}
