package runtimeenv

import (
	"context"
	"github.com/Autumn-27/artex/internal/toolrelay"
	tool "github.com/Autumn-27/norma/tool"
)

func (m *ToolManager) prepareAliases(ctx context.Context, rt *tool.ExecutionRuntime) (string, error) {
	entries := map[string]toolrelay.Entry{}
	for _, alias := range toolrelay.Aliases {
		name := alias
		if name == "python3" {
			name = "python"
		}
		spec := rt.Commands[name]
		entries[alias] = toolrelay.Entry{Executable: spec.Executable, PrefixArgs: spec.PrefixArgs}
	}
	return toolrelay.PrepareAt(ctx, m.parentRoot, m.options.RelayExecutable, entries, m.options.RelayDigest)
}

// RunRelay is retained for explicit development/test executable fixtures.
func RunRelay() (bool, int) { return toolrelay.Run() }
