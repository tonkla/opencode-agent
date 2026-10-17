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
	memoryFile = "MEMORY.md"
	userFile   = "USER.md"
	memoryDir  = "memory"
)

// Curated budgets in characters, after the Hermes limits. A write that would
// exceed the budget fails so the agent consolidates instead of growing the
// injected snapshot silently.
const (
	memoryBudget = 2200
	userBudget   = 1375
)

func memoryPath(dir, name string) string { return filepath.Join(dir, name) }

// validTarget reports whether name is a curated memory file.
func validTarget(name string) bool { return name == memoryFile || name == userFile }

func budgetFor(name string) int {
	if name == userFile {
		return userBudget
	}
	return memoryBudget
}

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

// Snapshot renders the bootstrap block injected when a session is created.
// It returns "" when both curated files are missing or empty, so sessions
// without memory behave exactly as before.
func Snapshot(dir string) string {
	mem := readCurated(dir, memoryFile)
	user := readCurated(dir, userFile)
	if mem == "" && user == "" {
		return ""
	}
	var b strings.Builder
	if mem != "" {
		fmt.Fprintf(&b, "MEMORY (durable facts) [%d/%d chars]\n%s\n\n", runeLen(mem), memoryBudget, mem)
	}
	if user != "" {
		fmt.Fprintf(&b, "USER (stable preferences) [%d/%d chars]\n%s\n\n", runeLen(user), userBudget, user)
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
	if runeLen(next) > budgetFor(name) {
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
