package update

import (
	"ddyidc/internal/shared/runtimepaths"
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
 ReclamationDigests map[string]string `json:"-"`
	GitPath             string
	GoPath              string
	NPMPath             string
	SystemctlPath       string
	SudoPath            string
	ServerService       string
	UpdateHelperService string
	MinFreeMB           uint64
	TimeoutSeconds      int
	HealthURL           string
	VersionURL          string
	SmokeURL            string
	ResultDir           string
	Backend             TargetConfig
	Frontend            TargetConfig
}

func (c Config) normalized() Config {
	if strings.TrimSpace(c.GitPath) == "" {
		c.GitPath = "/usr/bin/git"
	}
	if strings.TrimSpace(c.GoPath) == "" {
		c.GoPath = "/usr/local/go/bin/go"
	}
	if strings.TrimSpace(c.NPMPath) == "" {
		c.NPMPath = "/usr/local/bin/npm"
	}
	if strings.TrimSpace(c.SystemctlPath) == "" {
		c.SystemctlPath = "/usr/bin/systemctl"
	}
	if strings.TrimSpace(c.SudoPath) == "" {
		c.SudoPath = "/usr/bin/sudo"
	}
	if strings.TrimSpace(c.ServerService) == "" {
		c.ServerService = "ddyidc.service"
	}
	if strings.TrimSpace(c.UpdateHelperService) == "" {
		c.UpdateHelperService = "ddyidc-update-helper.service"
	}
	if c.MinFreeMB == 0 {
		c.MinFreeMB = 10 * 1024
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 600
	}
	if strings.TrimSpace(c.HealthURL) == "" {
		c.HealthURL = "http://127.0.0.1:8080/health"
	}
	if strings.TrimSpace(c.VersionURL) == "" {
		c.VersionURL = "http://127.0.0.1:8080/api/v1/system/version"
	}
	if strings.TrimSpace(c.SmokeURL) == "" {
		c.SmokeURL = "http://127.0.0.1:8080/api/v1/products"
	}
	if strings.TrimSpace(c.ResultDir) == "" {
		c.ResultDir = "/var/lib/ddyidc/update-results"
	}
	if strings.TrimSpace(c.Backend.Workspace) == "" {
		c.Backend.Workspace = "/var/lib/ddyidc/update/workspaces/backend"
	}
	if strings.TrimSpace(c.Backend.Executable) == "" {
		c.Backend.Executable = "/opt/ddyidc/application/ddyidc-server"
	}
	if strings.TrimSpace(c.Backend.ConfigFile) == "" {
		c.Backend.ConfigFile = "/etc/ddyidc/config.yaml"
	}
	if strings.TrimSpace(c.Backend.HelperPath) == "" {
		c.Backend.HelperPath = "/opt/ddyidc/bin/ddyidc-update-helper"
	}
	if strings.TrimSpace(c.Frontend.Workspace) == "" {
		c.Frontend.Workspace = "/var/lib/ddyidc/update/workspaces/frontend"
	}
	if strings.TrimSpace(c.Frontend.StaticURL) == "" {
		c.Frontend.StaticURL = "http://127.0.0.1:8081/"
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
		"SystemctlPath": c.SystemctlPath, "SudoPath": c.SudoPath,
		"ServerService": c.ServerService, "UpdateHelperService": c.UpdateHelperService,
		"HealthURL": c.HealthURL, "VersionURL": c.VersionURL, "SmokeURL": c.SmokeURL,
		"ResultDir": c.ResultDir, "Backend.Workspace": c.Backend.Workspace,
		"Backend.Executable": c.Backend.Executable, "Backend.ConfigFile": c.Backend.ConfigFile,
		"Backend.HelperPath": c.Backend.HelperPath, "Frontend.Workspace": c.Frontend.Workspace,
		"Frontend.StaticURL": c.Frontend.StaticURL,
	}
	for name, value := range map[string]string{"ServerService": c.ServerService, "UpdateHelperService": c.UpdateHelperService} {
		if !validServiceName(value) {
			return errors.New("更新配置中的 " + name + " 不是有效的 systemd 服务名")
		}
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return errors.New("更新配置缺少 " + name)
		}
	}
	for _, value := range []string{c.GitPath, c.GoPath, c.NPMPath, c.SystemctlPath, c.SudoPath, c.Backend.Workspace, c.Backend.Executable, c.Backend.ConfigFile, c.Backend.HelperPath, c.Frontend.Workspace, c.ResultDir} {
		if !filepath.IsAbs(value) {
			return errors.New("更新路径必须是绝对路径: " + value)
		}
	}
	return nil
}

func (c Config) helperPlanPath() string {
	return filepath.Join(c.ResultDir, "pending.plan.json")
}

func validServiceName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || !strings.HasSuffix(value, ".service") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_.@-", character) {
			continue
		}
		return false
	}
	return true
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

// UseDataDirectory rejects legacy writable paths instead of silently losing old data.
func (c *Config) UseDataDirectory(p runtimepaths.Paths) error {
	if c.ResultDir == "" {
		c.ResultDir = p.Join("update-results")
	}
	if c.Backend.Workspace == "" {
		c.Backend.Workspace = p.Join("update/workspaces/backend")
	}
	if c.Frontend.Workspace == "" {
		c.Frontend.Workspace = p.Join("update/workspaces/frontend")
	}
	for _, path := range []string{c.ResultDir, c.Backend.Workspace, c.Frontend.Workspace} {
		if err := p.Within(path); err != nil {
			return err
		}
	}
	return nil
}
