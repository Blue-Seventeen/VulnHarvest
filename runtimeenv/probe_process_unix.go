//go:build !windows

package runtimeenv

import (
	"os/exec"
	"syscall"
)

type unixProbeOwner struct{ group int }

func startProbeProcess(cmd *exec.Cmd) (probeProcessOwner, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &unixProbeOwner{group: cmd.Process.Pid}, nil
}

func (owner *unixProbeOwner) stopAndWait() error {
	// The group remains addressable after its leader exits. This kills inherited
	// descendants without depending on a still-living parent's PID/tree lookup.
	err := syscall.Kill(-owner.group, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
