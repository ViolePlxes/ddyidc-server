package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func verifyBuildInfrastructure(ctx context.Context, sudoPath string) error {
	root := "/opt/ddyidc/deploy"
	raw, e := os.ReadFile(filepath.Join(root, "release-infrastructure.json"))
	if e != nil {
		return fmt.Errorf("受限构建基础设施未安装: %w", e)
	}
	var hashes map[string]string
	if e = json.Unmarshal(raw, &hashes); e != nil {
		return e
	}
	required := map[string]string{"installed_launcher": buildControl, "installed_service_override": "/etc/systemd/system/ddyidc.service.d/95-build-isolation.conf"}
	required["installed_firewall"] = "/opt/ddyidc/bin/ddyidc-build-firewall"
	required["installed_firewall_unit"] = "/etc/systemd/system/ddyidc-build-firewall.service"
	for _, name := range []string{"database-identities.py", "nginx-ddyidc.conf", "build-control.py", "build-firewall.py", "install-build-isolation.py"} {
		required[name] = filepath.Join(root, name)
	}
	for name, path := range required {
		data, e := os.ReadFile(path)
		if e != nil {
			return fmt.Errorf("release infrastructure unreadable: %s", name)
		}
		sum := sha256.Sum256(data)
		if hashes[name] == "" || hex.EncodeToString(sum[:]) != hashes[name] {
			return fmt.Errorf("release infrastructure drift: %s", name)
		}
	}
	// Some production systems make /etc/sudoers.d root-only. Never weaken that
	// directory or reveal its contents to the API account just to check a hash.
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(bounded, sudoPath, "-n", buildControl, "verify", "0").Run(); err != nil {
		return fmt.Errorf("isolated build privilege rule verification failed: %w", err)
	}
	return nil
}
