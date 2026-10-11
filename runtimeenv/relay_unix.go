//go:build !windows

package runtimeenv

import (
	"fmt"
	"os"
	"syscall"
)

func relayExec(path string, args []string) int {
	err := syscall.Exec(path, append([]string{path}, args...), os.Environ())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool relay target could not start")
		return 126
	}
	return 0
}
