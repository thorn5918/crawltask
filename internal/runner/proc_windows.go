//go:build windows

package runner

import (
	"os/exec"
	"strconv"
)

func setSysProcAttr(*exec.Cmd) {}

// killTree Windows 下用 taskkill /T /F 终止整棵进程树。
func killTree(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
