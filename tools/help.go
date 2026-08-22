package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// HelpTool implements the "help" tool: the long version of a tool's
// documentation, on request.
//
// It exists because the two audiences for a tool's docs want opposite things.
// The schema description is sent with every single request, so it has to be
// short — which leaves no room for the worked example that actually teaches a
// tool, and the tools with the most leverage (lua, subagent) are exactly the
// ones that need one. Help pays that cost only when the model asks.
//
// Every registered tool is answerable, including MCP and .lua tools: where
// there is no long article the schema description and parameter list are
// returned instead, so "help" never comes back empty for a tool the model can
// see.
type HelpTool struct{}

func (t *HelpTool) Name() string { return "help" }

func (t *HelpTool) Run(ctx context.Context, input map[string]any) ToolResult {
	name := strings.TrimSpace(stringParam(input, "tool", ""))
	if name == "" {
		return okf("%s", helpIndex())
	}

	article, hasArticle := toolHelp[name]
	schema, hasSchema := schemaEntry(name)

	// A few articles describe something that spans several tools rather than
	// one ("jobs"). They have no schema entry and that is not an error: the
	// lifecycle is what a model needs explained, and no single tool owns it.

	if !hasArticle && !hasSchema {
		return failf("no tool called %q. Call help() with no arguments for the list.", name)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", name)
	if hasArticle {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(article))
		b.WriteString("\n")
	}
	if hasSchema {
		// Printed even when there is an article: the article explains when and
		// why, the schema is the authority on the exact parameter names, and a
		// hand-written article is the thing most likely to drift.
		b.WriteString("\n## Parameters\n")
		b.WriteString(describeSchema(schema))
	}
	if !hasArticle {
		b.WriteString("\nNo long-form article for this tool; the description above is all there is.\n")
	}
	if hasArticle && !hasSchema {
		b.WriteString("\n(A topic, not a tool — nothing to call by this name.)\n")
	}
	return okf("%s", b.String())
}

// helpIndex lists every available tool with the first sentence of its
// description, and says which ones have a long article worth reading.
func helpIndex() string {
	type row struct {
		name, summary string
		article       bool
	}
	var rows []row
	for _, entry := range GetAllToolsSchema() {
		fn, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)
		_, hasArticle := toolHelp[name]
		rows = append(rows, row{name: name, summary: firstSentence(desc), article: hasArticle})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var b strings.Builder
	b.WriteString("Available tools. Call help(tool=\"name\") for the full article, examples included.\n")
	b.WriteString("Start with help(\"jobs\") — it explains how parallel agents, notices, answers and locks fit together, which is the part of this environment you have no prior for.\n")
	for _, r := range rows {
		marker := " "
		if r.article {
			marker = "*"
		}
		fmt.Fprintf(&b, "\n%s %-16s %s", marker, r.name, r.summary)
	}
	b.WriteString("\n\n(* has a long article. The rest return their description and parameters.)")
	return b.String()
}

// schemaEntry finds one tool's schema entry by name, MCP tools included.
func schemaEntry(name string) (map[string]any, bool) {
	for _, entry := range GetAllToolsSchema() {
		fn, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		if n, _ := fn["name"].(string); n == name {
			return fn, true
		}
	}
	return nil, false
}

// describeSchema renders a tool's parameters as a readable list. Sorted, with
// the required ones marked, because a JSON Schema dump is harder to read than
// the thing it describes.
func describeSchema(fn map[string]any) string {
	desc, _ := fn["description"].(string)
	params, _ := fn["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)

	required := map[string]bool{}
	if list, ok := params["required"].([]any); ok {
		for _, r := range list {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	if list, ok := params["required"].([]string); ok {
		for _, r := range list {
			required[r] = true
		}
	}

	var b strings.Builder
	if len(props) == 0 {
		b.WriteString("(none)\n")
	} else {
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			spec, _ := props[n].(map[string]any)
			typ, _ := spec["type"].(string)
			if typ == "" {
				typ = "any"
			}
			text, _ := spec["description"].(string)
			mark := ""
			if required[n] {
				mark = " (required)"
			}
			fmt.Fprintf(&b, "- %s: %s%s", n, typ, mark)
			if text != "" {
				fmt.Fprintf(&b, " — %s", text)
			}
			b.WriteString("\n")
		}
	}
	if desc != "" {
		fmt.Fprintf(&b, "\n## Schema description\n%s\n", desc)
	}
	return b.String()
}

// firstSentence trims a description down to something that fits in a list.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	const max = 110
	if len(s) > max {
		if cut := strings.LastIndex(s[:max], " "); cut > 0 {
			return s[:cut] + "…"
		}
		return s[:max] + "…"
	}
	return s
}
