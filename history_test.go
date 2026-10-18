package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testClock(at ...time.Time) (func() time.Time, *int) {
	i := 0
	calls := new(int)
	return func() time.Time {
		*calls++
		t := at[i]
		if i < len(at)-1 {
			i++
		}
		return t
	}, calls
}

func TestHistoryAppendsBothSenders(t *testing.T) {
	clock, _ := testClock(
		time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC),
		time.Date(2026, 10, 18, 11, 30, 5, 0, time.UTC),
		time.Date(2026, 10, 18, 11, 30, 9, 0, time.UTC),
	)
	h := &chatHistory{baseDir: t.TempDir(), clock: clock, started: clock()}
	h.appendUser("hello")
	h.appendAgent("hi there")
	raw, err := os.ReadFile(filepath.Join(h.baseDir, "history", "20261018", "1130.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "**User** _11:30:05_: hello\n\n**Agent** _11:30:09_: hi there\n\n"
	if string(raw) != want {
		t.Errorf("got %q want %q", raw, want)
	}
}

func TestHistorySkipsEmpty(t *testing.T) {
	clock, calls := testClock(time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC))
	h := &chatHistory{baseDir: t.TempDir(), clock: clock, started: clock()}
	h.appendUser("")
	h.appendAgent("")
	if *calls != 1 {
		t.Errorf("empty lines must not touch the clock or disk, %d calls", *calls)
	}
	if _, err := os.Stat(filepath.Join(h.baseDir, "history")); !os.IsNotExist(err) {
		t.Error("empty lines must not create the history dir")
	}
}

func TestHistoryResetStartsNewFile(t *testing.T) {
	first := time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC)
	middle := time.Date(2026, 10, 18, 11, 30, 20, 0, time.UTC)
	second := time.Date(2026, 10, 18, 11, 45, 0, 0, time.UTC)
	last := time.Date(2026, 10, 18, 11, 45, 40, 0, time.UTC)
	clock, _ := testClock(first, middle, second, last)
	h := &chatHistory{baseDir: t.TempDir(), clock: clock, started: clock()}
	h.appendUser("before")
	h.reset()
	h.appendUser("after")
	before, err := os.ReadFile(filepath.Join(h.baseDir, "history", "20261018", "1130.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != "**User** _11:30:20_: before\n\n" {
		t.Errorf("old file polluted: %q", before)
	}
	after, err := os.ReadFile(filepath.Join(h.baseDir, "history", "20261018", "1145.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "**User** _11:45:40_: after\n\n" {
		t.Errorf("new file wrong: %q", after)
	}
}

func TestPathFor(t *testing.T) {
	got := pathFor("/ws", time.Date(2026, 10, 18, 9, 5, 0, 0, time.UTC))
	want := filepath.Join("/ws", "history", "20261018", "0905.md")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
