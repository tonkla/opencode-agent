package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
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

func TestSplitMarkdown(t *testing.T) {
	if got := splitMarkdown("hi", telegramLimit); len(got) != 1 || got[0] != "hi" {
		t.Errorf("short: %q", got)
	}
	long := strings.Repeat("x", maxReplyRunes*2+10)
	if got := splitMarkdown(long, telegramLimit); len(got) != 3 {
		t.Errorf("long: %d chunks", len(got))
	}
	emoji := strings.Repeat("😀", maxReplyRunes+1)
	got := splitMarkdown(emoji, telegramLimit)
	if len(got) != 2 || len([]rune(strings.Join(got, ""))) != maxReplyRunes+1 {
		t.Error("multibyte split broke runes")
	}
}

// TestSplitMarkdownBound proves converted chunks fit the Telegram cap
// even when markup expands the source.
func TestSplitMarkdownBound(t *testing.T) {
	src := strings.Repeat("**x** ", 2000)
	for _, p := range splitMarkdown(src, telegramLimit) {
		if n := len([]rune(telegramHTML(p))); n > telegramLimit {
			t.Fatalf("chunk of %d runes over the limit", n)
		}
	}
	if got := splitMarkdown(src, telegramLimit); len(got) < 2 {
		t.Errorf("marked-up long text stayed one chunk")
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

// TestAskWithProgressStreamsDeltas drives a whole turn against a fake v2
// server: the message list only ever shows the finished reply, while live
// text arrives on the SSE bus. Heartbeats, other sessions, empty deltas,
// and malformed frames are ignored; our session's deltas accumulate into
// growing partials ahead of the final answer.
func TestAskWithProgressStreamsDeltas(t *testing.T) {
	future := time.Now().UnixMilli() + 60000
	completed := future + 1
	var polls atomic.Int64
	var eventAuth atomic.Bool
	frames := []string{
		`data: {"id":"e0","type":"server.connected","data":{}}`,
		`: heartbeat`,
		`data: {"id":"e1","type":"session.text.delta","data":{"sessionID":"other","assistantMessageID":"m","ordinal":0,"delta":"nope"}}`,
		`data: {"id":"e2","type":"session.text.delta","data":{"sessionID":"s1","assistantMessageID":"m","ordinal":0,"delta":"Hello "}}`,
		`data: {"id":"e3","type":"session.text.delta","data":{"sessionID":"s1","assistantMessageID":"m","ordinal":0,"delta":""}}`,
		`data: {"id":"e4","type":"session.text.delta","data":{"sessionID":"s1","assistantMessageID":"m","ordinal":0,"delta":"world"}}`,
		`data: not json`,
		`data: {"id":"e5","type":"session.text.ended","data":{"sessionID":"s1","assistantMessageID":"m","ordinal":0,"text":"Hello world"}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api/event":
			if user, pass, ok := r.BasicAuth(); !ok || user != "u" || pass != "p" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			eventAuth.Store(true)
			w.Header().Set("Content-Type", "text/event-stream")
			for _, f := range frames {
				_, _ = w.Write([]byte(f + "\n\n"))
				w.(http.Flusher).Flush()
			}
		case p == "/api/session" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"data":{"id":"s1"}}`))
		case strings.HasSuffix(p, "/prompt"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(p, "/message"):
			if polls.Add(1) < 2 {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			msg := map[string]any{
				"type":    "assistant",
				"time":    map[string]any{"created": future, "completed": completed},
				"content": []any{map[string]any{"type": "text", "text": "Hello world"}},
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
	if reply != "Hello world" {
		t.Errorf("final reply = %q, want %q", reply, "Hello world")
	}
	if !eventAuth.Load() {
		t.Error("progress never subscribed to /api/event with credentials")
	}
	if len(got) != 2 || got[0] != "Hello " || got[1] != "Hello world" {
		t.Errorf("progress = %q, want [\"Hello \" \"Hello world\"]", got)
	}
}

// TestPreviewGateCoalesces pins the throttle: the first text shows, a
// repeat or a too-soon change does not, and the change shows once the
// interval passes.
func TestPreviewGateCoalesces(t *testing.T) {
	now := time.Now()
	g := &previewGate{interval: time.Second, clock: func() time.Time { return now }}
	if !g.allow("a") {
		t.Error("first update must show")
	}
	if g.allow("a") {
		t.Error("repeat must not show")
	}
	if g.allow("b") {
		t.Error("change inside the interval must not show")
	}
	now = now.Add(2 * time.Second)
	if !g.allow("b") {
		t.Error("change after the interval must show")
	}
}

// TestAnswerStreamsViaEdits drives a private-chat turn end to end: a "…"
// placeholder goes out, SSE deltas become throttled edits carrying the
// growing preview, and the placeholder becomes the final reply. No draft
// call may fire, and history keeps the whole reply.
func TestAnswerStreamsViaEdits(t *testing.T) {
	oldInterval := previewInterval
	previewInterval = 0
	defer func() { previewInterval = oldInterval }()
	future := time.Now().UnixMilli() + 60000
	completed := future + 1
	var polls atomic.Int64
	var mu sync.Mutex
	var sends, edits []string
	var drafts atomic.Int64
	botSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseMultipartForm(1 << 20)
		p := r.URL.Path
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(p, "sendMessageDraft"):
			drafts.Add(1)
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case strings.HasSuffix(p, "sendMessage"):
			sends = append(sends, r.FormValue("text"))
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":11,"chat":{"id":7,"type":"private"},"date":1,"text":"x"}}`))
		case strings.HasSuffix(p, "editMessageText"):
			edits = append(edits, r.FormValue("text"))
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":11,"chat":{"id":7,"type":"private"},"date":1,"text":"x"}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t"}}`))
		}
	}))
	defer botSrv.Close()
	b, err := bot.New("test-token", bot.WithServerURL(botSrv.URL))
	if err != nil {
		t.Fatal(err)
	}
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api/session" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"data":{"id":"s1"}}`))
		case strings.HasSuffix(p, "/prompt"):
			_, _ = w.Write([]byte(`{}`))
		case p == "/api/event":
			w.Header().Set("Content-Type", "text/event-stream")
			for _, f := range []string{
				`data: {"id":"e1","type":"session.text.delta","data":{"sessionID":"s1","assistantMessageID":"m","ordinal":0,"delta":"Hello "}}`,
				`data: {"id":"e2","type":"session.text.delta","data":{"sessionID":"s1","assistantMessageID":"m","ordinal":0,"delta":"world"}}`,
			} {
				_, _ = w.Write([]byte(f + "\n\n"))
				w.(http.Flusher).Flush()
			}
		case strings.HasSuffix(p, "/message"):
			if polls.Add(1) < 2 {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			msg := map[string]any{
				"type":    "assistant",
				"time":    map[string]any{"created": future, "completed": completed},
				"content": []any{map[string]any{"type": "text", "text": "Hello world"}},
			}
			raw, _ := json.Marshal(map[string]any{"data": []any{msg}})
			_, _ = w.Write(raw)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer agentSrv.Close()
	a := testApp(t, 7)
	a.baseURL = agentSrv.URL
	a.password = "p"
	a.timeout = 30 * time.Second
	update := &models.Update{Message: &models.Message{
		Text: "hi", From: &models.User{ID: 7}, Chat: models.Chat{ID: 7, Type: models.ChatTypePrivate},
	}}
	a.answer(context.Background(), b, update)
	if drafts.Load() != 0 {
		t.Errorf("draft endpoint hit %d times, previews must use edits", drafts.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sends) != 1 || sends[0] != "…" {
		t.Errorf("sends = %q, want one placeholder", sends)
	}
	if len(edits) < 2 {
		t.Fatalf("edits = %q, want previews plus final", edits)
	}
	for _, e := range edits[:len(edits)-1] {
		if !strings.Contains(e, "▌") {
			t.Errorf("preview edit %q misses the cursor", e)
		}
	}
	if last := edits[len(edits)-1]; last != "Hello world" {
		t.Errorf("final edit = %q, want the full reply", last)
	}
	sess, _ := a.sessionFor(7)
	raw, err := os.ReadFile(filepath.Join(sess.history.baseDir, "history", sess.history.started.Format("20060102"), sess.history.started.Format("1504")+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Hello world") {
		t.Errorf("history misses the reply: %q", raw)
	}
}
