//go:build !windows

package runtimeenv

import (
	"debug/elf"
	"debug/macho"
)

func fileMetadata(path string) (string, string) {
	if f, err := elf.Open(path); err == nil {
		defer f.Close()
		return f.Machine.String(), ""
	}
	if f, err := macho.Open(path); err == nil {
		defer f.Close()
		return f.Cpu.String(), ""
	}
	return "", ""
}
