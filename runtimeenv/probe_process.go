package runtimeenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type probeProcessOwner interface{ stopAndWait() error }

// Root Wait owns only the root process: explicit file pipes are drained by our
// own goroutine, so a normally exiting parent cannot hide behind descendant-held
// stdout while those descendants continue executing. Every exit/cancel path stops
// the platform owner, waits root, drains/closes output, and joins that goroutine.
func runOwnedProbe(ctx context.Context, cmd *exec.Cmd, out io.Writer) (error, error) {
	if ctx.Err() != nil {
		return ctx.Err(), nil
	}
	read, write, err := os.Pipe()
	if err != nil {
		return err, nil
	}
	defer read.Close()
	cmd.Stdout, cmd.Stderr = write, write
	copied := make(chan error, 1)
	go func() { _, err := io.Copy(out, read); copied <- err }()
	owner, err := startProbeProcess(cmd)
	_ = write.Close()
	if err != nil {
		<-copied
		return err, nil
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var runErr, cleanupErr error
	select {
	case runErr = <-waited:
		cleanupErr = owner.stopAndWait()
	case <-ctx.Done():
		cleanupErr = owner.stopAndWait()
		runErr = <-waited
	}
	select {
	case copyErr := <-copied:
		cleanupErr = errors.Join(cleanupErr, copyErr)
	case <-time.After(2 * time.Second):
		_ = read.Close()
		<-copied
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("probe output failed to drain"))
	}
	return runErr, cleanupErr
}
