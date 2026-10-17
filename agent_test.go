package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractAnswer(t *testing.T) {
	completed := int64(7)
	cases := []struct {
		name     string
		messages []sessionMessage
		want     string
		ok       bool
	}{
		{"none", nil, "", false},
		{"incomplete ignored", []sessionMessage{{Type: "assistant", Content: []contentPart{{Type: "text", Text: "hi"}}}}, "", false},
		{"text joined", []sessionMessage{{Type: "assistant", Time: messageTime{Completed: &completed},
			Content: []contentPart{{Type: "text", Text: "a"}, {Type: "tool", Text: "x"}, {Type: "text", Text: "b"}}}}, "a\nb", true},
		{"latest wins",
			[]sessionMessage{
				{Type: "assistant", Time: messageTime{Created: 1, Completed: &completed}, Content: []contentPart{{Type: "text", Text: "old"}}},
				{Type: "assistant", Time: messageTime{Created: 2, Completed: &completed}, Content: []contentPart{{Type: "text", Text: "new"}}},
			},
			"new", true},
		{"empty is marked", []sessionMessage{{Type: "assistant", Time: messageTime{Completed: &completed}}}, "(empty reply)", true},
	}
	for _, c := range cases {
		got, ok := extractAnswer(c.messages)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestSplitMessage(t *testing.T) {
	if got := splitMessage("hi"); len(got) != 1 || got[0] != "hi" {
		t.Errorf("short: %q", got)
	}
	long := strings.Repeat("x", maxReplyRunes*2+10)
	if got := splitMessage(long); len(got) != 3 {
		t.Errorf("long: %d chunks", len(got))
	}
	emoji := strings.Repeat("😀", maxReplyRunes+1)
	got := splitMessage(emoji)
	if len(got) != 2 || len([]rune(strings.Join(got, ""))) != maxReplyRunes+1 {
		t.Error("multibyte split broke runes")
	}
}

func TestCreateBodyPinsWorkspace(t *testing.T) {
	a := NewAgent(AgentOptions{Directory: "/tmp/ws"})
	loc, ok := a.createBody()["location"].(map[string]any)
	if !ok {
		t.Fatal("create body missing location; session lands in server dir")
	}
	if loc["directory"] != "/tmp/ws" {
		t.Errorf("location.directory = %v, want /tmp/ws", loc["directory"])
	}
}

func TestEnsureWorkspaceDir(t *testing.T) {
	if err := ensureWorkspaceDir(filepath.Join(t.TempDir(), "nested", "ws")); err != nil {
		t.Errorf("missing dir: %v", err)
	}
	if err := ensureWorkspaceDir(t.TempDir()); err != nil {
		t.Errorf("writable dir: %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkspaceDir(file); err == nil {
		t.Error("file path must fail")
	}
	readonly := t.TempDir()
	if err := os.Chmod(readonly, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(readonly, 0o755)
	if err := ensureWorkspaceDir(readonly); err == nil {
		t.Error("read-only dir must fail")
	} else if !strings.Contains(err.Error(), "not writable") {
		t.Errorf("error %q must name the permission problem", err)
	}
}
