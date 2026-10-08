package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const buildJobs = "/var/lib/ddyidc-build/jobs"
const buildControl = "/opt/ddyidc/bin/ddyidc-build-control"

func (m *Manager) prepareBuild(ctx context.Context, task *Task, target TargetConfig, info VersionInfo) (string, error) {
	job := filepath.Join(buildJobs, formatID(task.ID))
	if _, err := os.Stat(buildControl); err != nil {
		return "", fmt.Errorf("isolated build launcher is not installed: %w", err)
	}
	if err := os.Mkdir(job, os.ModeSetgid|0770); err != nil {
		return "", err
	}
	source := filepath.Join(job, "source")
	if err := os.Mkdir(source, os.ModeSetgid|0770); err != nil {
		return "", err
	}
	archive := filepath.Join(job, "source.tar")
	if output, err := m.git(ctx, target.Workspace, "archive", "--format=tar", "--output="+archive, info.RemoteCommit); err != nil {
		return "", fmt.Errorf("export candidate: %w (%s)", err, output)
	}
	if err := validateSourceArchive(archive); err != nil {
		return "", err
	}
	if output, err := m.runner.Run(ctx, job, "/usr/bin/tar", []string{"--no-same-owner", "--no-same-permissions", "-xf", archive, "-C", source}, nil); err != nil {
		return "", fmt.Errorf("extract candidate: %w (%s)", err, output)
	}
	if err := validateReleaseTree(source, false); err != nil {
		return "", err
	}
	// Grant only the dedicated build group access to this exported source.
	if err := filepath.WalkDir(job, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		mode := os.FileMode(0660)
		if entry.IsDir() {
			mode = os.ModeSetgid | 0770
		}
		return os.Chmod(path, mode)
	}); err != nil {
		return "", err
	}
	plan := map[string]string{"DDYIDC_VERSION": info.Remote, "DDYIDC_COMMIT": info.RemoteCommit, "DDYIDC_BRANCH": target.Branch, "DDYIDC_BUILD_TIME": time.Now().UTC().Format(time.RFC3339)}
	if err := m.files.WriteJSON(filepath.Join(job, "build.json"), plan); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(job, "build.json"), 0660); err != nil {
		return "", err
	}
	return job, nil
}

func (m *Manager) isolatedBuild(ctx context.Context, task *Task, action string) (string, error) {
	output, err := m.runner.Run(ctx, "/", m.config.SudoPath, []string{"-n", buildControl, action, formatID(task.ID)}, nil)
	if ctx.Err() != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if stopErr := m.stopBuild(cleanup, task.ID); stopErr != nil {
			return output, fmt.Errorf("build cancellation: %w; command: %v", stopErr, err)
		}
	}
	return output, err
}

func (m *Manager) stopBuild(ctx context.Context, taskID int64) error {
	_, err := m.runner.Run(ctx, "/", m.config.SudoPath, []string{"-n", buildControl, "stop", formatID(taskID)}, nil)
	return err
}
