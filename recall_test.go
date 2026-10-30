package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func memoryScope(t *testing.T) RecallScope {
	t.Helper()
	s, err := recallScope("memory")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func historyScope(t *testing.T) RecallScope {
	t.Helper()
	s, err := recallScope("history")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecallScopesStayIsolated(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "MEMORY.md", "shared token here\n")
	writeFixture(t, dir, "history/20240101/1200.md", "shared token here\n")
	mem, err := Recall(dir, memoryScope(t), "shared token", 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range mem {
		if strings.HasPrefix(h.RelPath, "history/") {
			t.Errorf("memory scope leaked history hit %q", h.RelPath)
		}
	}
	if len(mem) == 0 {
		t.Error("memory scope found nothing")
	}
	hist, err := Recall(dir, historyScope(t), "shared token", 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hist {
		if !strings.HasPrefix(h.RelPath, "history/") {
			t.Errorf("history scope leaked memory hit %q", h.RelPath)
		}
	}
	if len(hist) == 0 {
		t.Error("history scope found nothing")
	}
}

func TestRecallTokenAND(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "MEMORY.md", "alpha beta together\nonly alpha here\n")
	hits, err := Recall(dir, memoryScope(t), "alpha beta", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Line != 1 {
		t.Fatalf("token-AND must match only the line holding every token, got %v", hits)
	}
}

func TestRecallEmptyQueryErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Recall(dir, memoryScope(t), "   ", 30); err == nil {
		t.Error("empty query must fail, not dump the corpus")
	}
}

func TestRecallUnknownScopeErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out bytes.Buffer
	err := runRecall([]string{"nope", "--", "query"}, &out)
	if err == nil {
		t.Fatal("unknown scope must fail")
	}
	for _, want := range []string{"memory", "history"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must list valid key %q, got %q", want, err.Error())
		}
	}
}

func TestRunRecallScopeFirstFlagsAfter(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "MEMORY.md", "scope first flags after\n")
	t.Chdir(dir)
	var out bytes.Buffer
	if err := runRecall([]string{"memory", "--limit", "30", "--", "scope first"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "MEMORY.md:1: scope first flags after") {
		t.Errorf("recall must honor flags placed after the scope, got %q", out.String())
	}
}

func TestRecallByteIdentical(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "MEMORY.md", "deterministic recall check\nsecond line here\n")
	writeFixture(t, dir, "memory/2024-01-02.md", "deterministic recall note\n")
	render := func() string {
		hits, err := Recall(dir, memoryScope(t), "deterministic recall", 30)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, h := range hits {
			b.WriteString(h.String() + "\n")
		}
		return b.String()
	}
	if first, second := render(), render(); first != second || first == "" {
		t.Fatalf("repeated runs must render byte-identical output, got %q vs %q", first, second)
	}
}

func TestRecallPathDescending(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "memory/2024-01-01.md", "stamp token\n")
	writeFixture(t, dir, "memory/2024-01-02.md", "stamp token\n")
	hits, err := Recall(dir, memoryScope(t), "stamp token", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].RelPath != "memory/2024-01-02.md" || hits[1].RelPath != "memory/2024-01-01.md" {
		t.Fatalf("paths must sort descending so date stamps read newest first, got %v", hits)
	}
}

func TestRecallLimitCap(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "MEMORY.md", "cap one\ncap two\ncap three\n")
	hits, err := Recall(dir, memoryScope(t), "cap", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("limit 2 must cap output at 2 hits, got %v", hits)
	}
}

func TestSeedRecallCommands(t *testing.T) {
	dir := t.TempDir()
	if err := SeedRecallCommands(dir, "/bin/opencode-agent"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"recall-memory.md", "recall-history.md"} {
		raw, err := os.ReadFile(filepath.Join(dir, ".opencode", "commands", name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if !strings.HasPrefix(body, "---\ndescription: ") {
			t.Errorf("%s must carry description frontmatter, got:\n%s", name, body)
		}
		if strings.Contains(body, "`") {
			t.Errorf("%s must hold no backtick blocks, got:\n%s", name, body)
		}
		if !strings.Contains(body, "Query: $ARGUMENTS") {
			t.Errorf("%s must hold the query placeholder, got:\n%s", name, body)
		}
	}
	mem, err := os.ReadFile(filepath.Join(dir, ".opencode", "commands", "recall-memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mem), "/bin/opencode-agent recall memory --limit 30 -- QUERY") {
		t.Errorf("memory command must hold the fixed invocation, got:\n%s", mem)
	}
	edited := string(mem) + "user line\n"
	if err := os.WriteFile(filepath.Join(dir, ".opencode", "commands", "recall-memory.md"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SeedRecallCommands(dir, "/bin/opencode-agent"); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(dir, ".opencode", "commands", "recall-memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != edited {
		t.Error("re-seed must never overwrite a user-edited command file")
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(agents), recallMarker); got != 1 {
		t.Errorf("AGENTS.md section must appear exactly once, found %d markers", got)
	}
}
