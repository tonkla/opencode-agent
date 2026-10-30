package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// RecallScope names one searchable corpus: the roots walked under the
// workspace dir and the file globs matched relative to each root.
type RecallScope struct {
	Name  string
	Roots []string
	Globs []string
}

// recallScopes is the full registry of recall corpora. Memory covers the
// curated files plus daily notes; history covers session transcripts.
var recallScopes = map[string]RecallScope{
	"memory": {
		Name:  "memory",
		Roots: []string{"."},
		Globs: []string{"MEMORY.md", "USER.md", "SOUL.md", "IDENTITY.md", "memory/*.md"},
	},
	"history": {
		Name:  "history",
		Roots: []string{"history"},
		Globs: []string{"*.md", "*/*.md"},
	},
}

// Hit is one matching line. RelPath is slash-separated relative to dir.
type Hit struct {
	RelPath string
	Line    int
	Text    string
}

func (h Hit) String() string { return fmt.Sprintf("%s:%d: %s", h.RelPath, h.Line, h.Text) }

// recallScope resolves a scope name, naming the valid keys on failure.
func recallScope(name string) (RecallScope, error) {
	if s, ok := recallScopes[name]; ok {
		return s, nil
	}
	keys := make([]string, 0, len(recallScopes))
	for k := range recallScopes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return RecallScope{}, fmt.Errorf("unknown recall scope %q (valid: %s)", name, strings.Join(keys, ", "))
}

// Recall runs a deterministic line search over scope under dir: every query
// token must appear in the line under Unicode case folding. Results sort by
// relative path descending then line ascending, cut at limit. A negative
// limit means no cap. Pure over dir: no clock, no stat metadata, so
// identical query plus corpus gives identical output.
func Recall(dir string, scope RecallScope, query string, limit int) ([]Hit, error) {
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty query")
	}
	folded := make([]string, len(tokens))
	for i, t := range tokens {
		folded[i] = strings.ToLower(t)
	}
	var hits []Hit
	for _, root := range scope.Roots {
		base := filepath.Join(dir, root)
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return nil
			}
			matchRel, err := filepath.Rel(base, p)
			if err != nil {
				return nil
			}
			matched := false
			for _, g := range scope.Globs {
				if ok, _ := path.Match(g, filepath.ToSlash(matchRel)); ok {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			for i, line := range strings.Split(string(raw), "\n") {
				lower := strings.ToLower(line)
				keep := true
				for _, tok := range folded {
					if !strings.Contains(lower, tok) {
						keep = false
						break
					}
				}
				if keep {
					hits = append(hits, Hit{RelPath: relSlash, Line: i + 1, Text: line})
				}
			}
			return nil
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].RelPath != hits[j].RelPath {
			return hits[i].RelPath > hits[j].RelPath
		}
		return hits[i].Line < hits[j].Line
	})
	if limit >= 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// runRecall serves `opencode-agent recall <scope> --limit 30 -- <query>`
// from the current directory. It never touches the bot. The scope leads,
// so it is lifted before flag parsing: the stdlib flag set stops at the
// first positional argument and would otherwise swallow --limit.
func runRecall(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: recall <scope> --limit 30 -- <query>")
	}
	scope, err := recallScope(args[0])
	if err != nil {
		return err
	}
	fset := flag.NewFlagSet("recall", flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	limit := fset.Int("limit", 30, "max hits to print")
	if err := fset.Parse(args[1:]); err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	hits, err := Recall(dir, scope, strings.Join(fset.Args(), " "), *limit)
	if err != nil {
		return err
	}
	for _, h := range hits {
		if _, err := fmt.Fprintln(stdout, h.String()); err != nil {
			return err
		}
	}
	return nil
}

// recallMarker guards the AGENTS.md recall section so seeding appends once.
const recallMarker = "<!-- recall-commands -->"

func recallCommandFile(description, binPath, scope string) string {
	return "---\ndescription: " + description + "\n---\n\n" +
		"Search for the query below and summarize what you find.\n\n" +
		"Query: $ARGUMENTS\n\n" +
		"To get the matches, run the deterministic recall tool with the query as one single verbatim argument:\n\n" +
		binPath + " recall " + scope + " --limit 30 -- QUERY\n\n" +
		"Replace QUERY with the query text above, passed as one single verbatim argument. Report the matching lines, then answer from them.\n"
}

func recallAgentsSection(binPath string) string {
	return "## Recall\n\n" +
		"/recall-memory searches curated memory and daily notes. /recall-history searches session transcripts. " +
		"Both run the same deterministic search, so an identical query plus corpus gives identical output:\n\n" +
		binPath + " recall memory --limit 30 -- QUERY\n\n" +
		binPath + " recall history --limit 30 -- QUERY\n\n" +
		"In each invocation QUERY is one single verbatim argument.\n"
}

// writeOnce creates target with content, leaving a user-edited file alone.
func writeOnce(target, content string) error {
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

// SeedRecallCommands creates the OpenCode project commands plus one AGENTS.md
// section. It converges: existing files are never overwritten and the
// section is appended once behind recallMarker.
func SeedRecallCommands(dir, binPath string) error {
	cmdDir := filepath.Join(dir, ".opencode", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		return err
	}
	if err := writeOnce(filepath.Join(cmdDir, "recall-memory.md"),
		recallCommandFile("Search curated memory and daily notes for a query", binPath, "memory")); err != nil {
		return err
	}
	if err := writeOnce(filepath.Join(cmdDir, "recall-history.md"),
		recallCommandFile("Search past session transcripts for a query", binPath, "history")); err != nil {
		return err
	}
	agents := filepath.Join(dir, "AGENTS.md")
	raw, err := os.ReadFile(agents)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(raw), recallMarker) {
		return nil
	}
	var b strings.Builder
	b.Write(raw)
	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		b.WriteByte('\n')
	}
	if len(raw) > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(recallMarker + "\n")
	b.WriteString(recallAgentsSection(binPath))
	return os.WriteFile(agents, []byte(b.String()), 0o644)
}
