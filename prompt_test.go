package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeServe stands in for `opencode serve`. It records the last prompt text
// and answers every question with "ok".
type fakeServe struct {
	lastPrompt string
}

func (f *fakeServe) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"s1"}}`))
	})
	mux.HandleFunc("/api/session/s1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc("/api/session/s1/prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastPrompt = body.Text
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/session/s1/message", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UnixMilli()
		_ = json.NewEncoder(w).Encode(envelope[[]sessionMessage]{Data: []sessionMessage{{
			Type:    "assistant",
			Time:    messageTime{Created: now, Completed: &now},
			Content: []contentPart{{Type: "text", Text: "ok"}},
		}}})
	})
	return mux
}

func TestAskAfterResetInjectsSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, identityFile, "your name is Agent Gandalf"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeServe{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	a := NewAgent(AgentOptions{BaseURL: srv.URL, Directory: dir, Timeout: 5 * time.Second})
	ctx := context.Background()
	if err := a.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ask(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.lastPrompt, "Agent Gandalf") {
		t.Errorf("fresh post-reset prompt must carry IDENTITY.md, got %q", fake.lastPrompt)
	}
	if _, err := a.Ask(ctx, "hi again"); err != nil {
		t.Fatal(err)
	}
	if fake.lastPrompt != "hi again" {
		t.Errorf("snapshot must inject once, second prompt got %q", fake.lastPrompt)
	}
}
