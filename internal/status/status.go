package status

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
)

type Report struct {
	Targets []TargetStatus `json:"targets"`
}

type TargetStatus struct {
	ID          string        `json:"id"`
	Etcd        string        `json:"etcd,omitempty"`
	Type        string        `json:"type"`
	OutputRoot  string        `json:"output-root"`
	CurrentLink string        `json:"current-link"`
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
	report := Report{Targets: make([]TargetStatus, 0, len(cfg.Agent.Targets))}
	deployer := deploy.New()

	for _, target := range cfg.Agent.Targets {
		item := TargetStatus{
			ID:          target.ID,
			Etcd:        target.Etcd,
			Type:        target.Type,
			OutputRoot:  target.Output.Root,
			CurrentLink: target.Output.CurrentLink,
			Current:     CurrentStatus{Status: "unknown"},
		}
		item.Current = inspectCurrent(deployer, target.Output)
		report.Targets = append(report.Targets, item)
	}
	return report
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
