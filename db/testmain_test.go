package db

import (
	"github.com/Autumn-27/artex/internal/testenv"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("VH_FUTURE_CRASH_FIXTURE") != "" {
		os.Exit(m.Run())
	}
	os.Exit(testenv.RunSuite(m))
}
