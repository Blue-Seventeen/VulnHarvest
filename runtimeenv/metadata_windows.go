package runtimeenv

import (
	"debug/pe"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Read-only Windows VERSIONINFO calls never launch the GUI executable.
func fileMetadata(path string) (string, string) {
	var arch string
	if f, err := pe.Open(path); err == nil {
		switch f.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			arch = "amd64"
		case pe.IMAGE_FILE_MACHINE_ARM64:
			arch = "arm64"
		case pe.IMAGE_FILE_MACHINE_I386:
			arch = "386"
		}
		_ = f.Close()
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return arch, ""
	}
	dll := windows.NewLazySystemDLL("version.dll")
	sizeProc := dll.NewProc("GetFileVersionInfoSizeW")
	readProc := dll.NewProc("GetFileVersionInfoW")
	queryProc := dll.NewProc("VerQueryValueW")
	size, _, _ := sizeProc.Call(uintptr(unsafe.Pointer(name)), 0)
	if size == 0 || size > 1<<20 {
		return arch, ""
	}
	data := make([]byte, int(size))
	ok, _, _ := readProc.Call(uintptr(unsafe.Pointer(name)), 0, size, uintptr(unsafe.Pointer(&data[0])))
	if ok == 0 {
		return arch, ""
	}
	root, _ := windows.UTF16PtrFromString(`\`)
	var value unsafe.Pointer
	var length uint32
	ok, _, _ = queryProc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(root)), uintptr(unsafe.Pointer(&value)), uintptr(unsafe.Pointer(&length)))
	if ok == 0 || length < 52 || value == nil {
		return arch, ""
	}
	info := (*[13]uint32)(value)
	if info[0] != 0xfeef04bd {
		return arch, ""
	}
	return arch, fmt.Sprintf("%d.%d.%d.%d", info[2]>>16, info[2]&0xffff, info[3]>>16, info[3]&0xffff)
}
