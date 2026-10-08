package update

import (
	"os/exec"
	"syscall"
	"time"
)

// A compiler/package manager may leave grandchildren holding stdout open.
// Kill the entire group and bound pipe draining, not only its direct parent.
func configureProcessCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
}
