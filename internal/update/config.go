package update

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type TargetConfig struct {
	Workspace  string
	Branch     string
	Executable string `json:",optional"`
	ConfigFile string `json:",optional"`
	HelperPath string `json:",optional"`
	StaticURL  string `json:",optional"`
}

type Config struct {
	GitPath        string
	GoPath         string
	NPMPath        string
	MinFreeMB      uint64
	TimeoutSeconds int
	HealthURL      string
	VersionURL     string
	SmokeURL       string
	ResultDir      string
	Backend        TargetConfig
	Frontend       TargetConfig
}

func (c Config) normalized() Config {
	if strings.TrimSpace(c.GitPath) == "" {
		c.GitPath = "git"
	}
	if strings.TrimSpace(c.GoPath) == "" {
		c.GoPath = "go"
	}
	if strings.TrimSpace(c.NPMPath) == "" {
		c.NPMPath = "npm"
	}
	if c.MinFreeMB == 0 {
		c.MinFreeMB = 500
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 600
	}
	if strings.TrimSpace(c.Backend.Branch) == "" {
		c.Backend.Branch = "main"
	}
	if strings.TrimSpace(c.Frontend.Branch) == "" {
		c.Frontend.Branch = "main"
	}
	return c
}

func (c Config) timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }

func (c Config) validate() error {
	required := map[string]string{
		"GitPath": c.GitPath, "GoPath": c.GoPath, "NPMPath": c.NPMPath,
		"HealthURL": c.HealthURL, "VersionURL": c.VersionURL, "SmokeURL": c.SmokeURL,
		"ResultDir": c.ResultDir, "Backend.Workspace": c.Backend.Workspace,
		"Backend.Executable": c.Backend.Executable, "Backend.ConfigFile": c.Backend.ConfigFile,
		"Backend.HelperPath": c.Backend.HelperPath, "Frontend.Workspace": c.Frontend.Workspace,
		"Frontend.StaticURL": c.Frontend.StaticURL,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return errors.New("更新配置缺少 " + name)
		}
	}
	for _, value := range []string{c.Backend.Workspace, c.Backend.Executable, c.Backend.ConfigFile, c.Backend.HelperPath, c.Frontend.Workspace, c.ResultDir} {
		if !filepath.IsAbs(value) {
			return errors.New("更新路径必须是绝对路径: " + value)
		}
	}
	return nil
}

func (c Config) target(name string) (TargetConfig, error) {
	switch name {
	case TargetBackend:
		return c.Backend, nil
	case TargetFrontend:
		return c.Frontend, nil
	default:
		return TargetConfig{}, errors.New("不支持的更新目标")
	}
}
