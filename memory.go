package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Memory file names. Every memory artifact lives under the agent workspace
// directory, so sessions, notes, and curated facts share one root and no new
// config key is needed.
const (
	memoryFile   = "MEMORY.md"
	userFile     = "USER.md"
	soulFile     = "SOUL.md"
	identityFile = "IDENTITY.md"
	memoryDir    = "memory"
)

// Curated budgets in characters, after the Hermes limits. A write that would
// exceed the budget fails so the agent consolidates instead of growing the
// injected snapshot silently.
const (
	memoryBudget   = 2200
	userBudget     = 1375
	soulBudget     = 2200
	identityBudget = 1375
)

// artifactBudgets maps each writable workspace file to its char budget.
// SOUL.md and IDENTITY.md mirror the MEMORY.md and USER.md caps so total
// injected memory stays bounded no matter how large the files grow.
var artifactBudgets = map[string]int{
	memoryFile:   memoryBudget,
	userFile:     userBudget,
	soulFile:     soulBudget,
	identityFile: identityBudget,
}

// targetPrefixes maps a /remember or /forget prefix to its file. Bare text
// with no known prefix targets MEMORY.md.
var targetPrefixes = map[string]string{
	"memory":   memoryFile,
	"user":     userFile,
	"soul":     soulFile,
	"identity": identityFile,
}

func memoryPath(dir, name string) string { return filepath.Join(dir, name) }

// validTarget reports whether name is a writable workspace file.
func validTarget(name string) bool {
	_, ok := artifactBudgets[name]
	return ok
}

func budgetFor(name string) int { return artifactBudgets[name] }

func runeLen(s string) int { return len([]rune(s)) }

// readCurated returns the trimmed content of a curated file, or "" when the
// file does not exist yet. Missing memory is normal on first run.
func readCurated(dir, name string) string {
	raw, err := os.ReadFile(memoryPath(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// snapshotSections sets the fixed section order of the bootstrap block.
var snapshotSections = []struct{ file, title string }{
	{memoryFile, "MEMORY (durable facts)"},
	{userFile, "USER (stable preferences)"},
	{soulFile, "SOUL (personality and tone)"},
	{identityFile, "IDENTITY (agent name and vibe)"},
}

// truncateShort shrinks s to budget chars, marking the cut so readers know
// content was lost. Injection must degrade recall, never fail a session.
func truncateShort(s string, budget int) string {
	r := []rune(s)
	if len(r) <= budget {
		return s
	}
	const mark = "\n[truncated]"
	cut := budget - runeLen(mark)
	if cut < 0 {
		cut = 0
	}
	return string(r[:cut]) + mark
}

// Snapshot renders the bootstrap block injected when a session is created.
// It returns "" when every curated file is missing or empty, so sessions
// without memory behave exactly as before.
func Snapshot(dir string) string {
	var b strings.Builder
	for _, s := range snapshotSections {
		content := readCurated(dir, s.file)
		if content == "" {
			continue
		}
		content = truncateShort(content, budgetFor(s.file))
		fmt.Fprintf(&b, "%s [%d/%d chars]\n%s\n\n", s.title, runeLen(content), budgetFor(s.file), content)
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString("Maintain these files as you learn: durable facts go to MEMORY.md, preferences to USER.md, running notes to memory/YYYY-MM-DD.md.")
	return b.String()
}

// withSnapshot prefixes question with the memory snapshot when one exists.
// Pure over dir so tests pin it without a server.
func withSnapshot(dir, question string) string {
	if snap := Snapshot(dir); snap != "" {
		return snap + "\n\n---\n\n" + question
	}
	return question
}

// Remember appends one line to a curated file. It refuses the write when the
// entry would exceed the budget, naming current usage so the caller can
// consolidate first.
func Remember(dir, name, line string) error {
	if !validTarget(name) {
		return fmt.Errorf("unknown memory file %q", name)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fmt.Errorf("nothing to remember")
	}
	cur := readCurated(dir, name)
	next := line
	if cur != "" {
		next = cur + "\n" + line
	}
	if budget := budgetFor(name); budget > 0 && runeLen(next) > budget {
		return fmt.Errorf("%s at %d/%d chars: new entry (%d chars) exceeds the budget, consolidate entries first",
			name, runeLen(cur), budgetFor(name), runeLen(line))
	}
	return os.WriteFile(memoryPath(dir, name), []byte(next+"\n"), 0o644)
}

// Forget removes every line containing substr from a curated file and
// reports how many lines went.
func Forget(dir, name, substr string) (int, error) {
	if !validTarget(name) {
		return 0, fmt.Errorf("unknown memory file %q", name)
	}
	substr = strings.TrimSpace(substr)
	if substr == "" {
		return 0, fmt.Errorf("nothing to forget")
	}
	cur := readCurated(dir, name)
	if cur == "" {
		return 0, nil
	}
	var kept []string
	removed := 0
	for _, l := range strings.Split(cur, "\n") {
		if strings.Contains(l, substr) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	if err := os.WriteFile(memoryPath(dir, name), []byte(out), 0o644); err != nil {
		return 0, err
	}
	return removed, nil
}

func dailyPath(dir string, now time.Time) string {
	return filepath.Join(dir, memoryDir, now.Format("2006-01-02")+".md")
}

// AppendNote appends one timestamped line to today's daily note.
func AppendNote(dir, line string) error { return AppendNoteAt(dir, line, time.Now()) }

// AppendNoteAt is the testable core of AppendNote: pure over dir and clock.
func AppendNoteAt(dir, line string, now time.Time) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return fmt.Errorf("nothing to note")
	}
	path := dailyPath(dir, now)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "- %s %s\n", now.Format("15:04"), line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
