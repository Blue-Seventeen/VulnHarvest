package main

import (
	"github.com/Autumn-27/artex/internal/testenv"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testenv.RunSuite(m)) }
