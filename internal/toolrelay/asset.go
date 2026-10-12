package toolrelay

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"runtime"
)

// VerifyAsset binds the packaged executable to its build-time content and target.
func VerifyAsset(path, digest, goos, goarch string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("relay asset unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return fmt.Errorf("relay asset is not a regular executable")
	}
	// Windows build hosts do not expose Unix mode bits. The package writer
	// supplies 0755 and native startup verifies it on the destination host.
	if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		return fmt.Errorf("relay asset is not executable")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !validHash(digest) || Hash(b) != digest {
		return fmt.Errorf("relay asset content mismatch")
	}
	build, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("relay asset build metadata: %w", err)
	}
	if build.Path != "github.com/Autumn-27/artex/cmd/toolrelay" || len(build.Deps) != 0 {
		return fmt.Errorf("relay asset is not the standard-library-only helper")
	}
	settings := map[string]string{}
	for _, s := range build.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != goos || settings["GOARCH"] != goarch {
		return fmt.Errorf("relay asset platform mismatch")
	}
	return nil
}
