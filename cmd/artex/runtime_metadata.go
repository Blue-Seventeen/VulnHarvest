package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/runtimeenv"
)

func logToolProblems(snapshot runtimeenv.Snapshot) {
	names := make([]string, 0, len(snapshot.Tools))
	for name := range snapshot.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tool := snapshot.Tools[name]
		if tool.Status != "ready" || len(tool.Failures) > 0 {
			log.Print(config.Redact(fmt.Sprintf("[runtime] %s: %s (影响 %v)", tool.Name, tool.Status, tool.AffectedFeatures)))
			for _, failure := range tool.Failures {
				log.Print(config.Redact(fmt.Sprintf("[runtime] %s candidate %s: %s", tool.Name, failure.Source, failure.Reason)))
			}
		}
	}
}

func writeRuntimeMetadata(path string, metadata any) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.json")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	err = os.Rename(temporary, path)
	// Windows readers may briefly hold a handle without FILE_SHARE_DELETE.
	// Retry replacement rather than truncating the visible metadata in place.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32))) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		err = os.Rename(temporary, path)
	}
	return err
}
