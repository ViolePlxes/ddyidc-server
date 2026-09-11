package update

import (
	"fmt"
	"os/exec"
	"strings"
)

func startSystemdUnit(sudoPath, systemctlPath, serviceName string) error {
	if !validServiceName(serviceName) {
		return fmt.Errorf("无效的 systemd 服务名: %q", serviceName)
	}
	command := exec.Command(sudoPath, "-n", systemctlPath, "--no-block", "start", serviceName)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("启动更新助手服务: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
