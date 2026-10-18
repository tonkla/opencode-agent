package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

// TestKeepTypingRepeatsUntilStopped points the bot at a local server and
// counts sendChatAction calls: they must repeat on the interval and stop
// after stop.
func TestKeepTypingRepeatsUntilStopped(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendChatAction") {
			hits.Add(1)
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t"}}`))
	}))
	defer srv.Close()
	b, err := bot.New("test-token", bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	old := typingInterval
	typingInterval = 50 * time.Millisecond
	defer func() { typingInterval = old }()

	a := &app{}
	stop := a.keepTyping(context.Background(), b, 1)
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := hits.Load(); got < 3 {
		t.Fatalf("only %d typing sends, want repeats", got)
	}
	stop()
	atStop := hits.Load()
	time.Sleep(200 * time.Millisecond)
	if got := hits.Load(); got != atStop {
		t.Fatalf("typing continued after stop: %d then %d", atStop, got)
	}
}

// TestKeepTypingStopsWithParent proves a cancelled parent ends the loop
// even when the caller never stops it.
func TestKeepTypingStopsWithParent(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendChatAction") {
			hits.Add(1)
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t"}}`))
	}))
	defer srv.Close()
	b, err := bot.New("test-token", bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	old := typingInterval
	typingInterval = 50 * time.Millisecond
	defer func() { typingInterval = old }()

	a := &app{}
	ctx, cancel := context.WithCancel(context.Background())
	a.keepTyping(ctx, b, 1)
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	atCancel := hits.Load()
	time.Sleep(200 * time.Millisecond)
	if got := hits.Load(); got != atCancel {
		t.Fatalf("typing continued after parent cancel: %d then %d", atCancel, got)
	}
}
