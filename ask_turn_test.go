package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// replayTurn serves one scripted turn shaped like the captured live
// history: completed tool-only assistant messages arrive before the final
// text, and an idle marker closes the turn.
func replayTurn(t *testing.T, withText bool) (*Agent, func()) {
	t.Helper()
	var calls atomic.Int64
	toolMsg := func() map[string]any {
		now := time.Now().UnixMilli()
		return map[string]any{
			"type": "assistant",
			"time": map[string]any{"created": now, "completed": now},
			"content": []any{
				map[string]any{"type": "reasoning", "text": "let me use tools"},
				map[string]any{"type": "tool"},
			},
		}
	}
	textMsg := func() map[string]any {
		now := time.Now().UnixMilli()
		return map[string]any{
			"type": "assistant",
			"time": map[string]any{"created": now, "completed": now},
			"content": []any{
				map[string]any{"type": "reasoning", "text": "now write it"},
				map[string]any{"type": "text", "text": "the hexagon answer"},
			},
		}
	}
	idleMsg := func() map[string]any {
		return map[string]any{
			"type":    "idle",
			"time":    map[string]any{"created": time.Now().UnixMilli()},
			"outcome": "succeeded",
		}
	}
	userMsg := func() map[string]any {
		return map[string]any{
			"type": "user",
			"time": map[string]any{"created": time.Now().UnixMilli()},
			"text": "q",
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			write(map[string]any{"data": map[string]any{"id": "s1"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/s1":
			write(map[string]any{"data": map[string]any{}})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet:
			// Newest first, like the live API.
			n := calls.Add(1)
			msgs := []any{toolMsg(), toolMsg(), userMsg()}
			if withText && n >= 4 {
				msgs = append([]any{textMsg()}, msgs...)
			}
			if n >= 5 || (!withText && n >= 4) {
				msgs = append([]any{idleMsg()}, msgs...)
			}
			write(map[string]any{"data": msgs})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := NewAgent(AgentOptions{BaseURL: srv.URL, Directory: t.TempDir(), Timeout: 30 * time.Second})
	return a, func() {}
}

// TestAskWaitsForTurnText replays the live 👍 history: intermediate
// completed tool-only messages must not end the wait. The final text wins.
func TestAskWaitsForTurnText(t *testing.T) {
	a, _ := replayTurn(t, true)
	got, err := a.Ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if got != "the hexagon answer" {
		t.Fatalf("Ask returned %q, want the turn text", got)
	}
}

// TestAskToolOnlyTurnKeepsOkReply pins the preserved behavior: a turn that
// ends at the idle marker with no text still answers ok.
func TestAskToolOnlyTurnKeepsOkReply(t *testing.T) {
	a, _ := replayTurn(t, false)
	got, err := a.Ask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if got != okReply {
		t.Fatalf("tool-only turn returned %q, want %q", got, okReply)
	}
}
