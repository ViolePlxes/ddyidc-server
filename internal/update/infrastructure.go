package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func verifyBuildInfrastructure() error {
	root := "/opt/ddyidc/deploy"
	raw, e := os.ReadFile(filepath.Join(root, "release-infrastructure.json"))
	if e != nil {
		return fmt.Errorf("受限构建基础设施未安装: %w", e)
	}
	var hashes map[string]string
	if e = json.Unmarshal(raw, &hashes); e != nil {
		return e
	}
	required := map[string]string{"installed_launcher": buildControl, "installed_sudoers": "/etc/sudoers.d/ddyidc-isolated-build", "installed_service_override": "/etc/systemd/system/ddyidc.service.d/95-build-isolation.conf"}
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
	return nil
}
