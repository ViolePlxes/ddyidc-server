package update

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func validateReleaseTree(root string, artifact bool) error {
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, _ := filepath.Rel(root, path)
		name := strings.ToLower(entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release contains symbolic link: %s", relative)
		}
		if entry.IsDir() {
			if !artifact && (name == ".git" || name == "node_modules" || name == ".releases" || name == "bin" || name == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(filepath.ToSlash(relative), "public/") && strings.HasSuffix(name, ".sql") {
			return fmt.Errorf("database file in public source: %s", relative)
		}
		if name == ".env" || strings.HasPrefix(name, ".env.") && name != ".env.example" || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".sqlite") || strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".tar") || strings.HasSuffix(name, ".csv") || strings.HasSuffix(name, ".bak") {
			return fmt.Errorf("release contains private/runtime file: %s", relative)
		}
		if artifact {
			allowed := map[string]bool{".html": true, ".js": true, ".css": true, ".json": true, ".svg": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".ico": true, ".woff": true, ".woff2": true, ".ttf": true, ".txt": true, ".gif": true}
			marketingVideo := regexp.MustCompile(`^assets/(hero-reference|exp-main|exp-[1-6]|platform-preview|rem-solution-video)-[A-Za-z0-9_-]{8}\.mp4$`).MatchString(filepath.ToSlash(relative))
			if !allowed[strings.ToLower(filepath.Ext(name))] && !marketingVideo {
				return fmt.Errorf("unexpected public release file: %s", relative)
			}
			portalManifest := filepath.ToSlash(relative) == "portal/themes.json" || filepath.ToSlash(relative) == "portal/cloudflare-reference.json"
			if strings.HasSuffix(name, ".json") && name != "version.json" && name != "wallet-payment-channels.json" && !portalManifest {
				return fmt.Errorf("unregistered public data file: %s", relative)
			}
			backupComponent := filepath.ToSlash(filepath.Dir(relative)) == "assets" && regexp.MustCompile(`^backups-[a-z0-9_-]{8,}\.js$`).MatchString(name)
			if strings.Contains(name, "fixture") || strings.Contains(name, "backup") && !backupComponent || strings.Contains(name, "mock") {
				return fmt.Errorf("test/backup file in public release: %s", relative)
			}
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".js" || ext == ".html" || ext == ".json" || ext == ".txt" {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				text := string(data)
				if regexp.MustCompile(`TODO[0-9a-fA-F]{32}`).MatchString(text) {
					return fmt.Errorf("embedded business identifier in public release: %s", relative)
				}
				for _, marker := range []string{"BEGIN PRIVATE KEY", "BEGIN RSA PRIVATE KEY", "BEGIN OPENSSH PRIVATE KEY"} {
					if strings.Contains(text, marker) {
						return fmt.Errorf("private/test content in public release: %s", relative)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if artifact {
		return validatePortalReleaseAssets(root)
	}
	return nil
}

// ValidateReleaseTree is also used by the repository's mandatory CI gate.
func ValidateReleaseTree(root string, artifact bool) error {
	return validateReleaseTree(root, artifact)
}
