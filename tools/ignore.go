package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ignoreFileNames are the per-directory ignore files honored by glob/grep.
var ignoreFileNames = []string{".gitignore", ".aiignore"}

type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// gitignoreMatcher evaluates .gitignore/.aiignore rules with git semantics:
// per-directory rule sets, last-match-wins, negation and directory-only
// patterns. Rules are loaded lazily as the walk descends so nested ignore
// files apply only to their subtree.
type gitignoreMatcher struct {
	byDir map[string][]*ignoreRule // key: dir path relative to root ("" == root)
	seen  map[string]bool
}

func newGitignoreMatcher() *gitignoreMatcher {
	return &gitignoreMatcher{byDir: map[string][]*ignoreRule{}, seen: map[string]bool{}}
}

// newIgnoreMatcherFromInput returns a matcher unless the caller disabled
// ignore-file filtering via respectGitignore=false.
func newIgnoreMatcherFromInput(input map[string]any) *gitignoreMatcher {
	if !boolParam(input, "respectGitignore", true) {
		return nil
	}
	return newGitignoreMatcher()
}

// loadDir reads the ignore files that live directly in absDir once. dirRel is
// the directory's path relative to the walk root ("" for the root itself).
func (g *gitignoreMatcher) loadDir(absDir, dirRel string) {
	if g.seen[dirRel] {
		return
	}
	g.seen[dirRel] = true
	var rules []*ignoreRule
	for _, name := range ignoreFileNames {
		data, err := os.ReadFile(filepath.Join(absDir, name))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(data), "\n") {
			if r, ok := compileGitignorePattern(strings.TrimRight(ln, "\r")); ok {
				rules = append(rules, r)
			}
		}
	}
	if len(rules) > 0 {
		g.byDir[dirRel] = rules
	}
}

// Ignored reports whether the path (relative to the walk root, slash-separated)
// is excluded by any loaded ignore rule. Rules from ancestor directories are
// applied shallowest-first; the last matching rule wins.
func (g *gitignoreMatcher) Ignored(rel string, isDir bool) bool {
	ignored := false
	for _, dirRel := range ancestorDirs(rel) {
		rules := g.byDir[dirRel]
		if rules == nil {
			continue
		}
		sub := rel
		if dirRel != "" {
			sub = strings.TrimPrefix(rel, dirRel+"/")
		}
		for _, r := range rules {
			if r.dirOnly && !isDir {
				continue
			}
			if r.re.MatchString(sub) {
				ignored = !r.negate
			}
		}
	}
	return ignored
}

// ancestorDirs returns the directory prefixes that may hold ignore files
// applying to rel, from root ("") down to the immediate parent.
func ancestorDirs(rel string) []string {
	dirs := []string{""}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		dirs = append(dirs, strings.Join(parts[:i], "/"))
	}
	return dirs
}

// compileGitignorePattern converts one gitignore line into a rule. The second
// return is false for blank lines, comments and lines that carry no pattern.
func compileGitignorePattern(line string) (*ignoreRule, bool) {
	line = strings.TrimRight(line, " ")
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, false
	}
	negate := false
	if strings.HasPrefix(line, "!") {
		negate = true
		line = line[1:]
	}
	// Unescape a leading "\#" or "\!" that would otherwise be special.
	if strings.HasPrefix(line, "\\#") || strings.HasPrefix(line, "\\!") {
		line = line[1:]
	}
	dirOnly := strings.HasSuffix(line, "/")
	line = strings.TrimSuffix(line, "/")
	if line == "" {
		return nil, false
	}

	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimPrefix(line, "/")
	if !anchored && strings.Contains(line, "/") {
		anchored = true
	}

	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		// Unanchored patterns match at any depth.
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(line); {
		c := line[i]
		switch c {
		case '*':
			if i+1 < len(line) && line[i+1] == '*' {
				if i+2 < len(line) && line[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 3
				} else {
					b.WriteString(".*")
					i += 2
				}
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '\\', '{', '}':
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	// A matched entry also ignores everything nested beneath it.
	b.WriteString("(?:/.*)?$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, false
	}
	return &ignoreRule{re: re, negate: negate, dirOnly: dirOnly}, true
}

// ignoreNote renders the trailing hint shown when ignore files hid results.
func ignoreNote(hidden int) string {
	if hidden <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d hidden by .gitignore/.aiignore; set respectGitignore=false to include)", hidden)
}
