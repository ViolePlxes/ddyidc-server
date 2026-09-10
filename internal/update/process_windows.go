package update

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func startDetached(executable string, args []string, directory string) error {
	command := exec.Command(executable, args...)
	command.Dir = directory
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
