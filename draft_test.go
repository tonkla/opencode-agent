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

	"github.com/go-telegram/bot"
)

func TestLatestPartial(t *testing.T) {
	completed := int64(7)
	cases := []struct {
		name     string
		messages []sessionMessage
		want     string
		ok       bool
	}{
		{"none", nil, "", false},
		{"uncompleted streams", []sessionMessage{{Type: "assistant", Time: messageTime{Created: 1},
			Content: []contentPart{{Type: "text", Text: "half"}}}}, "half", true},
		{"completed streams too", []sessionMessage{{Type: "assistant", Time: messageTime{Created: 1, Completed: &completed},
			Content: []contentPart{{Type: "text", Text: "done"}}}}, "done", true},
		{"newest wins", []sessionMessage{
			{Type: "assistant", Time: messageTime{Created: 1}, Content: []contentPart{{Type: "text", Text: "old"}}},
			{Type: "assistant", Time: messageTime{Created: 2}, Content: []contentPart{{Type: "text", Text: "new"}}},
		}, "new", true},
		{"empty ignored", []sessionMessage{{Type: "assistant", Time: messageTime{Created: 1}}}, "", false},
		{"non-assistant ignored", []sessionMessage{{Type: "idle", Time: messageTime{Created: 2}}}, "", false},
	}
	for _, c := range cases {
		got, ok := latestPartial(c.messages)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestTruncateDraft(t *testing.T) {
	if got := truncateDraft("hi"); got != "hi" {
		t.Errorf("short: %q", got)
	}
	long := strings.Repeat("x", maxReplyRunes+10)
	if got := truncateDraft(long); len([]rune(got)) != maxReplyRunes {
		t.Errorf("long: %d runes", len([]rune(got)))
	}
	emoji := strings.Repeat("😀", maxReplyRunes+1)
	if got := truncateDraft(emoji); len([]rune(got)) != maxReplyRunes {
		t.Error("multibyte truncation broke runes")
	}
}

// TestWaitForAnswerStreamsProgress replays one slow turn: the first poll
// shows uncompleted partial text, the second completes it. The callback
// must see the partial and Ask must return the final answer.
func TestWaitForAnswerStreamsProgress(t *testing.T) {
	future := time.Now().UnixMilli() + 60000
	completed := future + 1
	var polls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api/session" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"data":{"id":"s1"}}`))
		case strings.HasSuffix(p, "/prompt"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(p, "/s1") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":{}}`))
		case strings.HasSuffix(p, "/message"):
			n := polls.Add(1)
			text, done := "half", false
			if n >= 2 {
				text, done = "half done", true
			}
			msg := map[string]any{
				"type":    "assistant",
				"time":    map[string]any{"created": future},
				"content": []any{map[string]any{"type": "text", "text": text}},
			}
			if done {
				msg["time"] = map[string]any{"created": future, "completed": completed}
			}
			raw, _ := json.Marshal(map[string]any{"data": []any{msg}})
			_, _ = w.Write(raw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := NewAgent(AgentOptions{BaseURL: srv.URL, Username: "u", Password: "p", Directory: t.TempDir(), Timeout: 30 * time.Second})
	var got []string
	reply, err := a.AskWithProgress(context.Background(), "q", func(partial string) { got = append(got, partial) })
	if err != nil {
		t.Fatal(err)
	}
	if reply != "half done" {
		t.Errorf("final reply = %q, want %q", reply, "half done")
	}
	if len(got) != 1 || got[0] != "half" {
		t.Errorf("progress = %q, want one partial %q", got, "half")
	}
}

// TestSendDraftHitsDraftEndpoint proves the preview reaches Telegram as a
// sendMessageDraft call carrying the draft id and text.
func TestSendDraftHitsDraftEndpoint(t *testing.T) {
	var gotID, gotText atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendMessageDraft") {
			_ = r.ParseMultipartForm(1 << 20)
			gotID.Store(r.FormValue("draft_id"))
			gotText.Store(r.FormValue("text"))
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
	(&app{}).sendDraft(context.Background(), b, 9, "42", "hi")
	if gotID.Load() != "42" || gotText.Load() != "hi" {
		t.Errorf("draft sent id=%v text=%v, want id=42 text=hi", gotID.Load(), gotText.Load())
	}
}
