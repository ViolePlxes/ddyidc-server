package main

import (
	"embed"
 "crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed deploy/linux/build-control.py deploy/linux/build-firewall.py deploy/linux/bootstrap-go.py deploy/linux/install-build-isolation.py deploy/linux/database-identities.py deploy/linux/nginx-ddyidc.conf deploy/linux/resource-reclamation.py deploy/linux/ddyidc-reclamation.service deploy/linux/ddyidc-reclamation.timer
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
	for _, name := range []string{"build-control.py", "build-firewall.py", "bootstrap-go.py", "install-build-isolation.py", "database-identities.py", "nginx-ddyidc.conf", "resource-reclamation.py", "ddyidc-reclamation.service", "ddyidc-reclamation.timer"} {
		data, e := releaseInfrastructure.ReadFile("deploy/linux/" + name)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
			return e
		}
	}
	args := []string{filepath.Join(dir, "install-build-isolation.py"), dir}
	if *reclamationTarget != "" { args = append(args, *reclamationTarget) }
	cmd := exec.Command("/usr/bin/python3", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func reclamationInfrastructureDigests() map[string]string {
 result:=map[string]string{}
 for _,name:=range []string{"resource-reclamation.py","ddyidc-reclamation.service","ddyidc-reclamation.timer"} {data,_:=releaseInfrastructure.ReadFile("deploy/linux/"+name);result[name]=fmt.Sprintf("%x",sha256.Sum256(data))}
 return result
}
