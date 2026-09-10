package update

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

type CommandRunner interface {
	Run(ctx context.Context, directory, executable string, args []string, env []string) (string, error)
}

type OSRunner struct{}

var credentialURLPattern = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)

func sanitizeOutput(value string) string {
	value = credentialURLPattern.ReplaceAllString(value, `${1}***@`)
	if len(value) > 64*1024 {
		value = value[len(value)-64*1024:]
	}
	return strings.TrimSpace(value)
}

func (OSRunner) Run(ctx context.Context, directory, executable string, args []string, env []string) (string, error) {
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("命令路径为空")
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = directory
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	output, err := cmd.CombinedOutput()
	return sanitizeOutput(string(output)), err
}

type FileOps interface {
	CopyDirectory(source, destination string) error
	ReplaceDirectory(source, destination string) error
	CopyFile(source, destination string) error
	RemoveAll(path string) error
	MkdirAll(path string) error
	WriteJSON(path string, value any) error
}
