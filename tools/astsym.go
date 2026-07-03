package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// readFileForAST reads a file for parsing, refusing files above HardMaxBytes so
// a huge file can't blow up the parser.
func readFileForAST(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > HardMaxBytes {
		return nil, fmt.Errorf("file too large for AST tools (%dKB > %dKB); use read/grep",
			info.Size()/1024, HardMaxBytes/1024)
	}
	return os.ReadFile(path)
}

// astsym.go: single-file code-intelligence backed by a pure-Go tree-sitter
// runtime (no CGO). Shared by three surfaces:
//   - read(outline=true)  -> structure map (signatures + line numbers)
//   - read(symbol=NAME)   -> exact body of one definition
//   - usages(name=NAME)   -> in-file references, classified, comments/strings excluded
//
// Language support is heuristic over tree-sitter node type names, which are
// reasonably consistent across grammars. Unsupported files degrade gracefully
// (outline/symbol fall back to a normal read; usages reports it).

// parseAST detects the language from the path and parses src into an AST.
// ok is false when the file type has no available grammar.
func parseAST(path string, src []byte) (root *ts.Node, lang *ts.Language, ok bool) {
	entry := grammars.DetectLanguage(path)
	if entry == nil || entry.Language == nil {
		return nil, nil, false
	}
	lang = entry.Language()
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil {
		return nil, nil, false
	}
	return tree.RootNode(), lang, true
}

// isDefinitionType reports whether a tree-sitter node type is a symbol definition.
// Covers containers and callables plus member-level declarations (constants and
// class/struct fields/properties) so the outline surfaces state, not just code.
func isDefinitionType(nt string) bool {
	if strings.Contains(nt, "use_declaration") || strings.Contains(nt, "import") {
		return false
	}
	kws := []string{"class", "interface", "trait", "enum", "struct",
		"method", "function", "type_declaration", "type_alias", "type_spec", "impl_item",
		"const", "property", "field"}
	suffixed := strings.HasSuffix(nt, "_declaration") || strings.HasSuffix(nt, "_definition") ||
		strings.HasSuffix(nt, "_item") || strings.HasSuffix(nt, "_spec")
	for _, k := range kws {
		if strings.Contains(nt, k) && (suffixed || nt == k) {
			return true
		}
	}
	return false
}

// isIdentifierType reports whether a node type names an identifier occurrence.
func isIdentifierType(nt string) bool {
	return nt == "name" || nt == "identifier" ||
		strings.HasSuffix(nt, "_identifier") || strings.HasSuffix(nt, "_name")
}

func langName(path string) string {
	if e := grammars.DetectLanguage(path); e != nil {
		return e.Name
	}
	return ""
}

// signatureRow returns the row where a definition's signature begins, skipping
// any leading attribute/decorator lines (e.g. PHP #[Attr], Python/JS decorators)
// that are part of the declaration node. Falls back to the node's own start row.
func signatureRow(n *ts.Node, lang *ts.Language) int {
	for i := 0; i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		ct := c.Type(lang)
		if strings.Contains(ct, "attribute") || strings.Contains(ct, "decorator") || ct == "comment" {
			continue
		}
		return int(c.StartPoint().Row)
	}
	return int(n.StartPoint().Row)
}

// astOutline returns a structure map of path. supported is false for files
// without an available grammar (caller should fall back to a normal read).
func astOutline(path string, src []byte) (content string, supported bool) {
	root, lang, ok := parseAST(path, src)
	if !ok {
		return "", false
	}
	srcLines := strings.Split(string(src), "\n")
	var out []string
	symbolCount := 0
	lastLine := -1
	ts.Walk(root, func(n *ts.Node, depth int) ts.WalkAction {
		if !n.IsNamed() || !isDefinitionType(n.Type(lang)) {
			return ts.WalkContinue
		}
		// Attributes/decorators (e.g. PHP #[Test], attribute lists) are part of the
		// declaration node, so its start row is the attribute line while the
		// signature sits below. Keep both: emit the attribute lines that precede
		// the signature, then the signature itself.
		declRow := int(n.StartPoint().Row)
		sigRow := signatureRow(n, lang)
		if sigRow == lastLine {
			return ts.WalkContinue // dedup wrapper+spec on same line (e.g. Go type_declaration>type_spec)
		}
		lastLine = sigRow
		symbolCount++
		for r := declRow; r <= sigRow; r++ {
			if r < 0 || r >= len(srcLines) {
				continue
			}
			line := strings.TrimRight(srcLines[r], " \t")
			out = append(out, fmt.Sprintf("%d| %s", r+1, line))
		}
		return ts.WalkContinue
	})

	// No symbols found means either an empty code file or a non-code file that
	// merely happened to match a grammar (e.g. .txt -> vimdoc). Fall back to a
	// normal read instead of returning a useless empty outline.
	if len(out) == 0 {
		return "", false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Outline of %s (%s) — %d symbols across %d lines. Read a range or symbol=NAME before editing.]\n",
		path, langName(path), symbolCount, len(srcLines))
	b.WriteString(strings.Join(out, "\n"))
	return b.String(), true
}

// astSymbol returns the exact source body of the named definition.
// supported is false when the file has no grammar; found is false when the
// symbol is not present.
func astSymbol(path string, src []byte, name string) (content string, supported, found bool) {
	root, lang, ok := parseAST(path, src)
	if !ok {
		return "", false, false
	}
	var node *ts.Node
	ts.Walk(root, func(n *ts.Node, depth int) ts.WalkAction {
		if node != nil {
			return ts.WalkStop
		}
		if n.IsNamed() && isDefinitionType(n.Type(lang)) {
			if nn := n.ChildByFieldName("name", lang); nn != nil && nn.Text(src) == name {
				node = n
				return ts.WalkStop
			}
		}
		return ts.WalkContinue
	})
	if node == nil {
		return "", true, false
	}
	start := int(node.StartPoint().Row) + 1
	end := int(node.EndPoint().Row) + 1
	return fmt.Sprintf("[%s in %s: lines %d-%d]\n%s", name, path, start, end, node.Text(src)), true, true
}

// astUsages lists in-file occurrences of name, classified as DEFINITION or use.
// Occurrences inside comments and string literals are not identifier nodes in
// the AST, so they are excluded — the precision a text search cannot match.
func astUsages(path string, src []byte, name string) (content string, supported bool) {
	root, lang, ok := parseAST(path, src)
	if !ok {
		return "", false
	}
	srcLines := strings.Split(string(src), "\n")
	var lines []string
	ts.Walk(root, func(n *ts.Node, depth int) ts.WalkAction {
		if !n.IsNamed() || !isIdentifierType(n.Type(lang)) || n.Text(src) != name {
			return ts.WalkContinue
		}
		row := int(n.StartPoint().Row)
		kind := "use"
		if p := n.Parent(); p != nil && isDefinitionType(p.Type(lang)) {
			if nn := p.ChildByFieldName("name", lang); nn != nil && nn.StartByte() == n.StartByte() {
				kind = "DEFINITION"
			}
		}
		text := ""
		if row < len(srcLines) {
			text = strings.TrimSpace(srcLines[row])
		}
		lines = append(lines, fmt.Sprintf("%d| [%s] %s", row+1, kind, text))
		return ts.WalkContinue
	})

	var b strings.Builder
	fmt.Fprintf(&b, "[Usages of %q in %s — %d hits (comments and strings excluded)]\n", name, path, len(lines))
	b.WriteString(strings.Join(lines, "\n"))
	return b.String(), true
}

// UsagesTool finds in-file references to a symbol using the AST.
type UsagesTool struct{}

func (t *UsagesTool) Name() string { return "usages" }

func (t *UsagesTool) Run(ctx context.Context, input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}
	name := stringParam(input, "name", "")
	if name == "" {
		return ToolResult{Type: "result", Success: false, Error: "name required"}
	}
	src, err := readFileForAST(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	content, supported := astUsages(path, src, name)
	if !supported {
		return ToolResult{Type: "result", Success: false,
			Error: fmt.Sprintf("usages: no grammar for %s; use grep instead", path)}
	}
	return ToolResult{Type: "result", Success: true, Content: content}
}
