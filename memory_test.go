package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotEmpty(t *testing.T) {
	if got := Snapshot(t.TempDir()); got != "" {
		t.Errorf("missing files must snapshot empty, got %q", got)
	}
}

func TestRememberAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, memoryFile, "project uses chi router"); err != nil {
		t.Fatal(err)
	}
	if err := Remember(dir, userFile, "prefers terse replies"); err != nil {
		t.Fatal(err)
	}
	snap := Snapshot(dir)
	for _, want := range []string{"MEMORY", "project uses chi router", "USER", "prefers terse replies", "MEMORY.md"} {
		if !strings.Contains(snap, want) {
			t.Errorf("snapshot missing %q:\n%s", want, snap)
		}
	}
}

func TestWithSnapshot(t *testing.T) {
	dir := t.TempDir()
	if got := withSnapshot(dir, "hi"); got != "hi" {
		t.Errorf("no memory must pass text through, got %q", got)
	}
	if err := Remember(dir, memoryFile, "project uses chi router"); err != nil {
		t.Fatal(err)
	}
	got := withSnapshot(dir, "hi")
	if !strings.HasSuffix(got, "\n\n---\n\nhi") || !strings.Contains(got, "project uses chi router") {
		t.Errorf("snapshot must prefix the question, got %q", got)
	}
}

func TestRememberRejectsEmptyAndUnknown(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, memoryFile, "  "); err == nil {
		t.Error("blank line must fail")
	}
	if err := Remember(dir, "NOTES.md", "x"); err == nil {
		t.Error("unknown file must fail")
	}
}

func TestRememberEnforcesBudget(t *testing.T) {
	dir := t.TempDir()
	full := strings.Repeat("a", memoryBudget-1)
	if err := Remember(dir, memoryFile, full); err != nil {
		t.Fatal(err)
	}
	if err := Remember(dir, memoryFile, "bb"); err == nil {
		t.Error("over-budget write must fail")
	} else if !strings.Contains(err.Error(), "exceeds the budget") {
		t.Errorf("error %q must name the budget", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, memoryFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != full {
		t.Error("rejected write must leave the file unchanged")
	}
}

func TestRememberBudgetCountsRunes(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, memoryFile, strings.Repeat("a", memoryBudget-3)); err != nil {
		t.Fatal(err)
	}
	// Two emoji are two chars but eight bytes: a byte count would refuse this.
	if err := Remember(dir, memoryFile, "😀😀"); err != nil {
		t.Errorf("rune budget must accept 2 chars: %v", err)
	}
}

func TestForget(t *testing.T) {
	dir := t.TempDir()
	for _, l := range []string{"project uses chi router", "staging ssh port 2222", "staging host is 10.0.1.50"} {
		if err := Remember(dir, memoryFile, l); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := Forget(dir, memoryFile, "staging")
	if err != nil || removed != 2 {
		t.Errorf("forget = (%d,%v), want (2,nil)", removed, err)
	}
	if snap := Snapshot(dir); strings.Contains(snap, "staging") || !strings.Contains(snap, "chi router") {
		t.Errorf("forget kept wrong lines:\n%s", snap)
	}
	if removed, err := Forget(dir, memoryFile, "nomatch"); err != nil || removed != 0 {
		t.Errorf("no-match forget = (%d,%v), want (0,nil)", removed, err)
	}
	if _, err := Forget(dir, memoryFile, "  "); err == nil {
		t.Error("blank substring must fail")
	}
}

func TestAppendNote(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	if err := AppendNoteAt(dir, "migrated db to postgres", now); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "memory", "2026-10-17.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "- 09:30 migrated db to postgres" {
		t.Errorf("note line = %q", got)
	}
	if err := AppendNoteAt(dir, "  ", now); err == nil {
		t.Error("blank note must fail")
	}
}
