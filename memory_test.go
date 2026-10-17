package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

func TestSoulAndIdentityShareBudgets(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, soulFile, strings.Repeat("v", soulBudget+1)); err == nil {
		t.Error("over-budget soul write must fail")
	}
	if err := Remember(dir, identityFile, strings.Repeat("v", identityBudget+1)); err == nil {
		t.Error("over-budget identity write must fail")
	}
	if err := Remember(dir, "NOTES.md", "x"); err == nil {
		t.Error("files outside the registry must fail")
	}
	if err := Remember(dir, soulFile, "direct, no sycophancy"); err != nil {
		t.Fatal(err)
	}
	removed, err := Forget(dir, soulFile, "sycophancy")
	if err != nil || removed != 1 {
		t.Errorf("soul forget = (%d,%v), want (1,nil)", removed, err)
	}
}

func TestSnapshotRendersAllSections(t *testing.T) {
	dir := t.TempDir()
	for name, line := range map[string]string{
		memoryFile:   "project uses chi router",
		userFile:     "prefers terse replies",
		soulFile:     "direct, no sycophancy",
		identityFile: "keeper bot",
	} {
		if err := Remember(dir, name, line); err != nil {
			t.Fatal(err)
		}
	}
	snap := Snapshot(dir)
	for _, want := range []string{"MEMORY (durable facts)", "USER (stable preferences)",
		"SOUL (personality and tone)", "IDENTITY (agent name and vibe)"} {
		if !strings.Contains(snap, want) {
			t.Errorf("snapshot missing section %q", want)
		}
	}
}

func TestSnapshotTruncatesWithMarker(t *testing.T) {
	dir := t.TempDir()
	// Bypass the write cap to simulate a file that grew outside /remember.
	long := strings.Repeat("w", soulBudget+100)
	if err := os.WriteFile(filepath.Join(dir, soulFile), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := Snapshot(dir)
	if !strings.Contains(snap, "[truncated]") {
		t.Error("over-cap file must carry the truncation marker")
	}
	if strings.Contains(snap, long) {
		t.Error("full over-cap content must not render")
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

func TestWorkspaceInventory(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	writes := map[string]string{
		memoryFile:   "project uses chi router",
		userFile:     "prefers terse replies",
		soulFile:     "direct, no sycophancy",
		identityFile: "keeper bot",
	}
	for name, line := range writes {
		if err := Remember(dir, name, line); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendNoteAt(dir, "migrated db to postgres", now); err != nil {
		t.Fatal(err)
	}
	if _, err := Forget(dir, memoryFile, "chi router"); err != nil {
		t.Fatal(err)
	}
	var names []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	want := []string{"IDENTITY.md", "MEMORY.md", "SOUL.md", "USER.md", filepath.Join("memory", "2026-10-17.md")}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("workspace files = %v, want %v", names, want)
	}
	raw, err := os.ReadFile(filepath.Join(dir, memoryFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "chi router") {
		t.Error("forgotten line must be gone from disk")
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
