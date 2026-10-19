package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
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

func TestIsNewCommand(t *testing.T) {
	for text, want := range map[string]bool{
		"/new": true, "/new@agentbot": true, "/new please": true,
		"/news": false, "/remember x": false, "talk about /new": false,
		" /new": false, "": false,
	} {
		if got := isNewCommand(text); got != want {
			t.Errorf("isNewCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestIsStartCommand(t *testing.T) {
	for text, want := range map[string]bool{
		"/start": true, "/start@agentbot": true, "/start please": true,
		"/starts": false, "/remember x": false, "talk about /start": false,
		" /start": false, "": false,
	} {
		if got := isStartCommand(text); got != want {
			t.Errorf("isStartCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestRequireUserSkipsStart(t *testing.T) {
	clock, _ := testClock(time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC))
	a := &app{config: Config{AllowedUserID: 7}, history: &chatHistory{baseDir: t.TempDir(), clock: clock, started: clock()}}
	called := false
	next := bot.HandlerFunc(func(context.Context, *bot.Bot, *models.Update) { called = true })
	update := &models.Update{Message: &models.Message{
		Text: "/start", From: &models.User{ID: 7}, Chat: models.Chat{ID: 7},
	}}
	a.requireUser(next)(context.Background(), nil, update)
	if !called {
		t.Error("guarded handler must still run for /start")
	}
	if _, err := os.Stat(filepath.Join(a.history.baseDir, "history")); !os.IsNotExist(err) {
		t.Error("/start must leave no history behind")
	}
}

func TestStartSkipsHistory(t *testing.T) {
	var hits atomic.Int64
	b := testBot(t, &hits)
	clock, _ := testClock(time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC))
	a := &app{history: &chatHistory{baseDir: t.TempDir(), clock: clock, started: clock()}}
	update := &models.Update{Message: &models.Message{
		Text: "/start", From: &models.User{ID: 7}, Chat: models.Chat{ID: 7},
	}}
	a.start(context.Background(), b, update)
	if hits.Load() != 1 {
		t.Fatalf("start reply must still send, %d sends", hits.Load())
	}
	if _, err := os.Stat(filepath.Join(a.history.baseDir, "history")); !os.IsNotExist(err) {
		t.Error("/start reply must leave no history behind")
	}
}

func TestRequireUserSkipsNew(t *testing.T) {
	clock, _ := testClock(time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC))
	a := &app{config: Config{AllowedUserID: 7}, history: &chatHistory{baseDir: t.TempDir(), clock: clock, started: clock()}}
	called := false
	next := bot.HandlerFunc(func(context.Context, *bot.Bot, *models.Update) { called = true })
	update := &models.Update{Message: &models.Message{
		Text: "/new", From: &models.User{ID: 7}, Chat: models.Chat{ID: 7},
	}}
	a.requireUser(next)(context.Background(), nil, update)
	if !called {
		t.Error("guarded handler must still run for /new")
	}
	if _, err := os.Stat(filepath.Join(a.history.baseDir, "history")); !os.IsNotExist(err) {
		t.Error("/new must leave no history behind")
	}
	update.Message.Text = "hello"
	a.requireUser(next)(context.Background(), nil, update)
	raw, err := os.ReadFile(filepath.Join(a.history.baseDir, "history", "20261018", "1130.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "**User**") || !strings.Contains(string(raw), "hello") {
		t.Errorf("plain text must be logged: %q", raw)
	}
}

func testBot(t *testing.T, hits *atomic.Int64) *bot.Bot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			hits.Add(1)
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
	return b
}

func TestSendReplySilentSkipsHistory(t *testing.T) {
	var hits atomic.Int64
	b := testBot(t, &hits)
	clock, _ := testClock(time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC))
	a := &app{history: &chatHistory{baseDir: t.TempDir(), clock: clock, started: clock()}}
	a.sendReplySilent(context.Background(), b, 1, "Fresh session started.")
	if hits.Load() != 1 {
		t.Fatalf("silent reply must still send, %d sends", hits.Load())
	}
	if _, err := os.Stat(filepath.Join(a.history.baseDir, "history")); !os.IsNotExist(err) {
		t.Error("silent reply must leave no history behind")
	}
	a.sendReply(context.Background(), b, 1, "logged")
	raw, err := os.ReadFile(filepath.Join(a.history.baseDir, "history", "20261018", "1130.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "logged") {
		t.Errorf("plain reply must be logged: %q", raw)
	}
}

func TestPathFor(t *testing.T) {
	got := pathFor("/ws", time.Date(2026, 10, 18, 9, 5, 0, 0, time.UTC))
	want := filepath.Join("/ws", "history", "20261018", "0905.md")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
