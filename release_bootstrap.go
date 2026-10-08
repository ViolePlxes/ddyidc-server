package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed deploy/linux/build-control.py deploy/linux/build-firewall.py deploy/linux/bootstrap-go.py deploy/linux/install-build-isolation.py deploy/linux/database-identities.py deploy/linux/nginx-ddyidc.conf
var releaseInfrastructure embed.FS

func installReleaseInfrastructure() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("release infrastructure bootstrap requires root")
	}
	dir, err := os.MkdirTemp("", "ddyidc-release-infrastructure-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"build-control.py", "build-firewall.py", "bootstrap-go.py", "install-build-isolation.py", "database-identities.py", "nginx-ddyidc.conf"} {
		data, e := releaseInfrastructure.ReadFile("deploy/linux/" + name)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
			return e
		}
	}
	cmd := exec.Command("/usr/bin/python3", filepath.Join(dir, "install-build-isolation.py"), dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
