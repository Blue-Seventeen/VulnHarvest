package toolrelay

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var finalPathName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

func canonicalPath(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Unlike EvalSymlinks, this resolves junction ancestors as well as 8.3
	// spellings. FILE_NAME_NORMALIZED | VOLUME_NAME_DOS are both zero.
	buf := make([]uint16, 32768)
	n, _, callErr := finalPathName.Call(f.Fd(), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if n == 0 {
		return "", fmt.Errorf("resolve relay root: %w", callErr)
	}
	if n >= uintptr(len(buf)) {
		return "", fmt.Errorf("relay root exceeds PATH bounds")
	}
	real := syscall.UTF16ToString(buf[:n])
	if strings.HasPrefix(real, `\\?\UNC\`) {
		real = `\\` + strings.TrimPrefix(real, `\\?\UNC\`)
	} else {
		real = strings.TrimPrefix(real, `\\?\`)
	}
	return real, nil
}
