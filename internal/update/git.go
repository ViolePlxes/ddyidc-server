package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (m *Manager) git(ctx context.Context, workspace string, args ...string) (string, error) {
	return m.runner.Run(ctx, workspace, m.config.GitPath, args, nil)
}

func (m *Manager) fetch(ctx context.Context, target TargetConfig) (string, error) {
	return m.git(ctx, target.Workspace, "fetch", "--tags", "origin", target.Branch)
}

func (m *Manager) versionInfo(ctx context.Context, name string, fetchRemote bool) (VersionInfo, error) {
	target, err := m.config.target(name)
	if err != nil {
		return VersionInfo{}, err
	}
	if fetchRemote {
		if _, err = m.fetch(ctx, target); err != nil {
			return VersionInfo{}, fmt.Errorf("git fetch %s: %w", name, err)
		}
	}
	branch, err := m.git(ctx, target.Workspace, "branch", "--show-current")
	if err != nil {
		return VersionInfo{}, err
	}
	currentCommit, err := m.git(ctx, target.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return VersionInfo{}, err
	}
	remoteRef := "origin/" + target.Branch
	remoteCommit, err := m.git(ctx, target.Workspace, "rev-parse", remoteRef)
	if err != nil {
		return VersionInfo{}, fmt.Errorf("读取远端分支 %s: %w", target.Branch, err)
	}
	currentVersion, _ := m.git(ctx, target.Workspace, "describe", "--tags", "--always", "--dirty")
	remoteVersion, _ := m.git(ctx, target.Workspace, "describe", "--tags", "--always", remoteRef)
	if name == TargetFrontend {
		currentVersion = packageVersion(filepath.Join(target.Workspace, "package.json"), currentVersion)
		if raw, showErr := m.git(ctx, target.Workspace, "show", remoteRef+":package.json"); showErr == nil {
			remoteVersion = packageVersionJSON([]byte(raw), remoteVersion)
		}
	}
	updatedAt, _ := m.git(ctx, target.Workspace, "show", "-s", "--format=%aI", "HEAD")
	ahead, behind := 0, 0
	if counts, countErr := m.git(ctx, target.Workspace, "rev-list", "--left-right", "--count", "HEAD..."+remoteRef); countErr == nil {
		fields := strings.Fields(counts)
		if len(fields) == 2 {
			ahead, _ = strconv.Atoi(fields[0])
			behind, _ = strconv.Atoi(fields[1])
		}
	}
	changes := []Change{}
	if logText, logErr := m.git(ctx, target.Workspace, "log", "--max-count=10", "--format=%H%x1f%s%x1f%aI", "HEAD.."+remoteRef); logErr == nil && strings.TrimSpace(logText) != "" {
		for _, line := range strings.Split(logText, "\n") {
			parts := strings.Split(line, "\x1f")
			if len(parts) == 3 {
				changes = append(changes, Change{Commit: parts[0], Feature: parts[1], Time: parts[2]})
			}
		}
	}
	return VersionInfo{Target: name, Current: currentVersion, Remote: remoteVersion, CurrentCommit: currentCommit, RemoteCommit: remoteCommit, Branch: branch, UpdatedAt: updatedAt, HasUpdate: currentCommit != remoteCommit && behind > 0, Ahead: ahead, Behind: behind, Changes: changes}, nil
}

func packageVersion(path, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return packageVersionJSON(data, fallback)
}

func packageVersionJSON(data []byte, fallback string) string {
	var value struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &value) == nil && strings.TrimSpace(value.Version) != "" {
		return "v" + strings.TrimPrefix(strings.TrimSpace(value.Version), "v")
	}
	return fallback
}

func (m *Manager) pull(ctx context.Context, target TargetConfig) (string, error) {
	return m.git(ctx, target.Workspace, "pull", "--ff-only", "origin", target.Branch)
}

func (m *Manager) resetWorktree(ctx context.Context, target TargetConfig, commit string) error {
	if strings.TrimSpace(commit) == "" {
		return fmt.Errorf("回滚 commit 为空")
	}
	_, err := m.git(ctx, target.Workspace, "reset", "--hard", commit)
	return err
}

func (m *Manager) featureAt(ctx context.Context, target TargetConfig, commit string) string {
	value, err := m.git(ctx, target.Workspace, "show", "-s", "--format=%s", commit)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
