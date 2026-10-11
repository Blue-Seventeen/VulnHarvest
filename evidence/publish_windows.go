//go:build windows

package evidence

import "golang.org/x/sys/windows"

// File.Sync already flushed the staged body. Windows cannot fsync directory
// handles via os.File.Sync; MoveFileEx WRITE_THROUGH durably publishes the rename
// on the same volume instead. Do not suppress any publication/permission error.
func publishBody(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
