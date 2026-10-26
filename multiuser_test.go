package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestIsAllowed(t *testing.T) {
	a := testApp(t, 123, 456)
	if !a.isAllowed(123) || !a.isAllowed(456) {
		t.Error("allowlisted users must pass")
	}
	if a.isAllowed(789) {
		t.Error("outsider must not pass")
	}
}

func TestUserDir(t *testing.T) {
	got := userDir("/ws", 123)
	want := filepath.Join("/ws", "123")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSessionForIsolatesWorkspaces(t *testing.T) {
	a := testApp(t, 123, 456)
	s123, err := a.sessionFor(123)
	if err != nil {
		t.Fatal(err)
	}
	s456, err := a.sessionFor(456)
	if err != nil {
		t.Fatal(err)
	}
	if s123 == s456 {
		t.Fatal("users must not share a session")
	}
	if s123.agent == s456.agent {
		t.Error("users must not share an Agent")
	}
	if s123.agent.directory == s456.agent.directory {
		t.Error("users must not share a directory")
	}
	if !strings.HasSuffix(s123.agent.directory, filepath.Join("123")) {
		t.Errorf("user dir wrong: %q", s123.agent.directory)
	}
	// Same user returns the same session.
	again, err := a.sessionFor(123)
	if err != nil {
		t.Fatal(err)
	}
	if again != s123 {
		t.Error("repeat lookup must return the same session")
	}
}

func TestPerUserMemoryIsolation(t *testing.T) {
	a := testApp(t, 123, 456)
	s123, _ := a.sessionFor(123)
	s456, _ := a.sessionFor(456)
	if err := Remember(s123.agent.directory, memoryFile, "alice secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s456.agent.directory, memoryFile))
	if err == nil && strings.Contains(string(raw), "alice secret") {
		t.Error("user 456 must not see user 123 memory")
	}
	if got := readCurated(s123.agent.directory, memoryFile); !strings.Contains(got, "alice secret") {
		t.Errorf("user 123 memory lost: %q", got)
	}
}

func TestRequireUserRejectsOutsider(t *testing.T) {
	a := testApp(t, 123)
	called := false
	next := bot.HandlerFunc(func(context.Context, *bot.Bot, *models.Update) { called = true })
	update := &models.Update{Message: &models.Message{
		Text: "hello", From: &models.User{ID: 999}, Chat: models.Chat{ID: 999},
	}}
	a.requireUser(next)(context.Background(), nil, update)
	if called {
		t.Error("outsider must not reach the handler")
	}
	if _, err := os.Stat(filepath.Join(a.baseDir, "999")); !os.IsNotExist(err) {
		t.Error("outsider must not get a workspace dir")
	}
}

func TestPerUserHistoryIsolation(t *testing.T) {
	a := testApp(t, 123, 456)
	s123, _ := a.sessionFor(123)
	s456, _ := a.sessionFor(456)
	s123.history.appendUser("alice says hi")
	s456.history.appendUser("bob says hi")
	raw123, err := os.ReadFile(filepath.Join(s123.history.baseDir, "history", s123.history.started.Format("20060102"), s123.history.started.Format("1504")+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw123), "alice says hi") || strings.Contains(string(raw123), "bob says hi") {
		t.Errorf("user 123 history polluted: %q", raw123)
	}
}
