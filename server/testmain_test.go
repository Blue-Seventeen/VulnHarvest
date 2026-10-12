package server

import (
	"github.com/Autumn-27/artex/internal/testenv"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("ARTEX_MCP_SENTINEL") != "" {
		os.Exit(m.Run())
	}
	os.Exit(testenv.RunSuite(m))
}
