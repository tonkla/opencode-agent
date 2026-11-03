package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestAskAfter404RetryInjectsSnapshotOnce(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, identityFile, "your name is Agent Gandalf"); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	var promptCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api/session" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"data":{"id":"s1"}}`))
		case p == "/api/session/s1" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":{}}`))
		case strings.HasSuffix(p, "/prompt"):
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			prompts = append(prompts, body.Text)
			if promptCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(p, "/message"):
			now := time.Now().UnixMilli()
			_ = json.NewEncoder(w).Encode(envelope[[]sessionMessage]{Data: []sessionMessage{{
				Type:    "assistant",
				Time:    messageTime{Created: now, Completed: &now},
				Content: []contentPart{{Type: "text", Text: "ok"}},
			}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := NewAgent(AgentOptions{BaseURL: srv.URL, Directory: dir, Timeout: 5 * time.Second})
	ctx := context.Background()
	if _, err := a.Ask(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 {
		t.Fatalf("first Ask must prompt twice (initial 404 plus retry), got %d", len(prompts))
	}
	if _, err := a.Ask(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 3 {
		t.Fatalf("second Ask must prompt once more, got %d prompts", len(prompts))
	}
	if prompts[2] != "second" {
		t.Errorf("snapshot must inject once per session, third prompt got %q", prompts[2])
	}
}
