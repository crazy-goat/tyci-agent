package agentdefs

import (
	"embed"
	"fmt"
	"sort"
)

// builtinFS embeds the shipped-with-the-binary agent definitions so tyci
// works out of the box with no setup: no download, no network access, and
// no dependency on the working directory at runtime. Sync (see sync.go)
// unpacks these into GlobalDir() on startup.
//
//go:embed builtin/*.md
var builtinFS embed.FS

// builtinDirName is the directory inside builtinFS holding the shipped
// definitions, and also the on-disk name Sync writes them under.
const builtinDirName = "builtin"

// Builtin parses every embedded definition and returns them sorted by name.
// It exists for two reasons beyond Sync's own use: it is the one place a
// broken frontmatter in a builtin file becomes a build/test failure instead
// of a file that silently disappears from a user's `agent list` (LoadDir
// swallows parse errors — see LoadDir's doc comment), and it gives tests a
// way to assert on the exact set of definitions tyci ships.
func Builtin() ([]Def, error) {
	entries, err := builtinFS.ReadDir(builtinDirName)
	if err != nil {
		return nil, fmt.Errorf("read embedded builtin dir: %w", err)
	}

	defs := make([]Def, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := builtinFS.ReadFile(builtinDirName + "/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", entry.Name(), err)
		}
		def, err := Parse(entry.Name(), data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded %s: %w", entry.Name(), err)
		}
		defs = append(defs, def)
	}

	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}
