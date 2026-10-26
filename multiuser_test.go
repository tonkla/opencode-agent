package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestScrubPaths(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "srv", "ws")
	dir := filepath.Join(base, "123")
	if got := scrubPaths(base, dir, "saved to "+dir+"/MEMORY.md"); got != "saved to ~/MEMORY.md" {
		t.Errorf("user dir must scrub to ~, got %q", got)
	}
	if got := scrubPaths(base, dir, "root "+base+" holds all users"); got != "root ~ holds all users" {
		t.Errorf("base dir must scrub to ~, got %q", got)
	}
	if got := scrubPaths(base, dir, "no paths here"); got != "no paths here" {
		t.Errorf("clean text must pass through, got %q", got)
	}
	if got := scrubPaths("", "", "keep /etc/hosts"); got != "keep /etc/hosts" {
		t.Errorf("empty dirs must not scrub, got %q", got)
	}
}

func TestSendReplyScrubsWorkspacePath(t *testing.T) {
	var replies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("bad sendMessage form: %v", err)
			}
			replies = append(replies, r.FormValue("text"))
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1,"type":"private"},"date":1,"text":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t"}}`))
	}))
	t.Cleanup(srv.Close)
	b, err := bot.New("test-token", bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	a := testApp(t, 7)
	dir := userDir(a.baseDir, 7)
	a.sendReply(context.Background(), b, 7, 7, "saved to "+dir+"/MEMORY.md")
	if len(replies) != 1 {
		t.Fatalf("%d sends, want 1", len(replies))
	}
	if strings.Contains(replies[0], a.baseDir) {
		t.Errorf("reply leaks workspace path: %q", replies[0])
	}
	sess, _ := a.sessionFor(7)
	raw, err := os.ReadFile(filepath.Join(sess.history.baseDir, "history", sess.history.started.Format("20060102"), sess.history.started.Format("1504")+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), a.baseDir) {
		t.Errorf("history leaks workspace path: %q", raw)
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
