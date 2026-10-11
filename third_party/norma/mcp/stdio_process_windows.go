package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func prepareStdioProcess(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x00000200 // CREATE_NEW_PROCESS_GROUP
	return nil
}

func stopStdioProcess(cmd *exec.Cmd) {
	// Use an absolute OS helper; controlled tool PATH must not replace tree kill.
	if root := os.Getenv("SystemRoot"); root != "" {
		_ = exec.Command(filepath.Join(root, "System32", "taskkill.exe"), "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	_ = cmd.Process.Kill()
}
