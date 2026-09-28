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

func TestExampleConfigHasRepresentativeTarget(t *testing.T) {
	got := ExampleConfig().Agent.Targets
	if len(got) != 1 || got[0].ID != "nginx" {
		t.Fatalf("ExampleConfig() targets = %#v, want one nginx target", got)
	}
}
