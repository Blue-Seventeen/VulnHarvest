// Command toolrelay is the portable standard-library-only relay asset.
package main

import (
	"fmt"
	"github.com/Autumn-27/artex/internal/toolrelay"
	"os"
)

func main() {
	handled, code := toolrelay.Run()
	if !handled {
		fmt.Fprintln(os.Stderr, "tool relay must be invoked through a verified runtime alias")
		code = 126
	}
	os.Exit(code)
}
