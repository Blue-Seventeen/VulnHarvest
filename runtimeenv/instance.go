package runtimeenv

import (
	"fmt"
	"net"
	"os"
	"sync"
)

// InstanceLock is held by the OS until Close or process death. The lock file
// intentionally remains: unlinking a held Unix lock allows a second inode owner.
type InstanceLock struct {
	file *os.File
	once sync.Once
	err  error
}

func LockInstance(path string) (*InstanceLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("portable instance already running or lock unavailable: %w", err)
	}
	return &InstanceLock{file: f}, nil
}
func (l *InstanceLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}

// Listen transfers a retained listener to the caller, without check/rebind.
// Only portable loopback addresses may fall back to an OS-assigned port.
func Listen(mode, address string) (net.Listener, error) {
	if mode != "portable" {
		return net.Listen("tcp", address)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("portable listeners require a numeric loopback address")
	}
	ln, err := net.Listen("tcp", address)
	if err == nil {
		return ln, nil
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}
