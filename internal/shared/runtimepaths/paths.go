// Package runtimepaths owns every application-writable directory.
package runtimepaths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultRoot = "/var/lib/ddyidc"

type Config struct {
	DataDir string `json:",optional"`
}
type Paths struct{ Root string }

func Resolve(c Config) (Paths, error) {
	root := strings.TrimSpace(c.DataDir)
	if root == "" {
		root = DefaultRoot
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return Paths{}, fmt.Errorf("Runtime.DataDir must be a specific absolute directory")
	}
	return Paths{filepath.Clean(root)}, nil
}
func (p Paths) Join(parts ...string) string {
	return filepath.Join(append([]string{p.Root}, parts...)...)
}

// Within checks existing symlink ancestors as well as lexical containment.
func (p Paths) Within(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("runtime path must be absolute: %s", path)
	}
	rel, err := filepath.Rel(p.Root, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("runtime path must be inside Runtime.DataDir: %s", path)
	}
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return err
	}
	ancestor := filepath.Clean(path)
	for {
		resolved, e := filepath.EvalSymlinks(ancestor)
		if e == nil {
			relative, e := filepath.Rel(root, resolved)
			if e != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("runtime symlink escapes data directory: %s", path)
			}
			return nil
		}
		if !os.IsNotExist(e) {
			return e
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return fmt.Errorf("cannot resolve runtime path")
		}
		ancestor = parent
	}
}
func (p Paths) Prepare() error {
	if err := os.MkdirAll(p.Root, 0750); err != nil {
		return err
	}
	for _, name := range []string{"logs", "storage/certification", "storage/ticket-attachments", "storage/geoip", "tmp", "home", "cache/go-build", "cache/go-mod", "cache/npm", "run", "update-results/staging", "update-results/backups", "update/workspaces"} {
		path := p.Join(name)
		if err := p.Within(path); err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	}
	for key, value := range map[string]string{"TMPDIR": p.Join("tmp"), "TMP": p.Join("tmp"), "TEMP": p.Join("tmp"), "HOME": p.Join("home"), "XDG_CACHE_HOME": p.Join("cache"), "GOCACHE": p.Join("cache/go-build"), "GOMODCACHE": p.Join("cache/go-mod"), "NPM_CONFIG_CACHE": p.Join("cache/npm")} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	if path := os.Getenv("DDYIDC_UPSTREAM_AUDIT_LOG"); path != "" {
		return p.Within(path)
	}
	return nil
}
