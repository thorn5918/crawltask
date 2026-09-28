//go:build !windows

package runner

import (
	"os/exec"
	"strconv"
	"syscall"
)

func setSysProcAttr(cmd *exec.Cmd) {
	// 独立进程组，便于按组强杀整棵进程树
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 终止整个进程组。
func killTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
