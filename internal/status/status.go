package status

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/state"
)

type Report struct {
	StateDir string         `json:"state-dir"`
	Targets  []TargetStatus `json:"targets"`
}

type TargetStatus struct {
	ID          string        `json:"id"`
	Type        string        `json:"type"`
	OutputRoot  string        `json:"output-root"`
	CurrentLink string        `json:"current-link"`
	StateStatus string        `json:"state-status"`
	State       state.Target  `json:"state"`
	StateError  string        `json:"state-error,omitempty"`
	Current     CurrentStatus `json:"current"`
}

type CurrentStatus struct {
	Status      string `json:"status"`
	Symlink     string `json:"symlink,omitempty"`
	ReleaseDir  string `json:"release-dir,omitempty"`
	LocalDigest string `json:"local-digest,omitempty"`
	Error       string `json:"error,omitempty"`
}

func Build(cfg config.Config) Report {
	report := Report{StateDir: cfg.Agent.StateDir, Targets: make([]TargetStatus, 0, len(cfg.Agent.Targets))}
	store, storeErr := state.New(cfg.Agent.StateDir)
	deployer := deploy.New()

	for _, target := range cfg.Agent.Targets {
		item := TargetStatus{
			ID:          target.ID,
			Type:        target.Type,
			OutputRoot:  target.Output.Root,
			CurrentLink: target.Output.CurrentLink,
			Current:     CurrentStatus{Status: "unknown"},
		}
		if storeErr != nil {
			item.StateStatus = "error"
			item.StateError = storeErr.Error()
		} else {
			item.StateStatus, item.State, item.StateError = inspectState(store, target.ID)
		}
		item.Current = inspectCurrent(deployer, target.Output)
		report.Targets = append(report.Targets, item)
	}
	return report
}

func inspectState(store *state.Store, targetID string) (string, state.Target, string) {
	exists, err := store.Exists(targetID)
	if err != nil {
		return "error", state.Target{}, err.Error()
	}
	if !exists {
		return "missing", state.Target{}, ""
	}
	saved, err := store.Load(targetID)
	if err != nil {
		return "error", state.Target{}, err.Error()
	}
	return "present", saved, ""
}

func inspectCurrent(deployer *deploy.Deployer, output config.Output) CurrentStatus {
	linkPath := filepath.Join(output.Root, output.CurrentLink)
	info, err := os.Lstat(linkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return CurrentStatus{Status: "missing"}
		}
		return CurrentStatus{Status: "error", Error: fmt.Sprintf("stat current link: %v", err)}
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return CurrentStatus{Status: "invalid", Error: "current path is not a symlink"}
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		return CurrentStatus{Status: "invalid", Error: fmt.Sprintf("read current symlink: %v", err)}
	}
	result := CurrentStatus{Status: "invalid", Symlink: target}
	digest, err := deployer.CurrentDigest(output)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		result.Error = fmt.Sprintf("resolve current symlink: %v", err)
		return result
	}
	result.Status = "active"
	result.ReleaseDir = resolved
	result.LocalDigest = digest
	return result
}
