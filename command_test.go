package main

import (
	"context"
	"encoding/json"
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

func TestMemoryTarget(t *testing.T) {
	file, rest := memoryTarget("project uses chi")
	if file != memoryFile || rest != "project uses chi" {
		t.Errorf("bare text must target MEMORY.md, got (%q,%q)", file, rest)
	}
	file, rest = memoryTarget("user: prefers terse replies")
	if file != userFile || rest != "prefers terse replies" {
		t.Errorf("user: prefix must target USER.md, got (%q,%q)", file, rest)
	}
	for prefix, want := range map[string]string{
		"soul: direct, no sycophancy":    soulFile,
		"identity: keeper bot":           identityFile,
		"memory: project uses chi":       memoryFile,
		"SOUL: uppercase still matches":  soulFile,
		"note: this colon is content":    memoryFile,
		"http://x is not a prefix match": memoryFile,
	} {
		file, _ := memoryTarget(prefix)
		if file != want {
			t.Errorf("%q must target %s, got %s", prefix, want, file)
		}
	}
	if _, rest := memoryTarget("user:"); rest != "" {
		t.Errorf("bare prefix must leave empty rest, got %q", rest)
	}
}

func TestMenuCommands(t *testing.T) {
	got := menuCommands()
	want := []string{"start", "ping", "new", "model", "usage", "remember", "forget", "help"}
	if len(got) != len(want) {
		t.Fatalf("menu holds %d commands, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Command != name {
			t.Errorf("menu[%d] = %q, want %q", i, got[i].Command, name)
		}
		if got[i].Description == "" {
			t.Errorf("menu[%d] %q needs a description", i, name)
		}
	}
}

func TestPublishCommands(t *testing.T) {
	var path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "setMyCommands") {
			path = r.URL.Path
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("bad setMyCommands form: %v", err)
			}
			var commands []struct {
				Command     string `json:"command"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal([]byte(r.FormValue("commands")), &commands); err != nil {
				t.Errorf("bad setMyCommands commands: %v", err)
			}
			raw, _ := json.Marshal(commands)
			body = string(raw)
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t"}}`))
	}))
	t.Cleanup(srv.Close)
	b, err := bot.New("test-token", bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{}
	a.publishCommands(context.Background(), b)
	if path == "" {
		t.Fatal("publish must call setMyCommands")
	}
	for _, name := range []string{"start", "ping", "new", "model", "usage", "remember", "forget", "help"} {
		if !strings.Contains(body, `"command":"`+name+`"`) {
			t.Errorf("menu payload misses %q: %s", name, body)
		}
	}
}

func TestHelpText(t *testing.T) {
	got := helpText()
	for _, c := range menuCommands() {
		if !strings.Contains(got, "/"+c.Command+" - "+c.Description) {
			t.Errorf("help text misses /%s: %q", c.Command, got)
		}
	}
}

func TestIsHelpCommand(t *testing.T) {
	for text, want := range map[string]bool{
		"/help": true, "/help@agentbot": true, "/help me": true,
		"/helps": false, "/remember x": false, "talk about /help": false,
		" /help": false, "": false,
	} {
		if got := isHelpCommand(text); got != want {
			t.Errorf("isHelpCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestHelpSkipsHistory(t *testing.T) {
	var hits atomic.Int64
	b := testBot(t, &hits)
	clock, _ := testClock(time.Date(2026, 10, 18, 11, 30, 0, 0, time.UTC))
	a := testAppWithClock(t, clock, 7)
	update := &models.Update{Message: &models.Message{
		Text: "/help", From: &models.User{ID: 7}, Chat: models.Chat{ID: 7},
	}}
	a.requireUser(a.help)(context.Background(), b, update)
	if hits.Load() != 1 {
		t.Fatalf("help reply must still send, %d sends", hits.Load())
	}
	if _, err := os.Stat(filepath.Join(userHistoryBase(a, 7), "history")); !os.IsNotExist(err) {
		t.Error("/help round trip must leave no history behind")
	}
}

func TestIsUnknownCommand(t *testing.T) {
	for text, want := range map[string]bool{
		"/bogus": true, "/bogus args": true, "/bogus@agentbot": true,
		"/new": false, "/start": false, "/ping": false, "/model": false,
		"/model list": false, "/usage": false, "/remember x": false, "/forget x": false, "/help": false,
		"hello": false, "talk about /bogus": false, "/": false, "/ ": false, "": false,
	} {
		if got := isUnknownCommand(text); got != want {
			t.Errorf("isUnknownCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestAnswerBlocksUnknownCommand(t *testing.T) {
	var agentHits atomic.Int64
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentHits.Add(1)
		http.Error(w, "must not reach the agent", http.StatusInternalServerError)
	}))
	t.Cleanup(agentSrv.Close)
	var reply string
	botSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("bad sendMessage form: %v", err)
			}
			reply = r.FormValue("text")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1,"type":"private"},"date":1,"text":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t"}}`))
	}))
	t.Cleanup(botSrv.Close)
	b, err := bot.New("test-token", bot.WithServerURL(botSrv.URL))
	if err != nil {
		t.Fatal(err)
	}
	a := testApp(t, 7)
	a.baseURL = agentSrv.URL
	a.password = "p"
	a.timeout = time.Second
	sess, err := a.sessionFor(7)
	if err != nil {
		t.Fatal(err)
	}
	sess.agent = NewAgent(AgentOptions{BaseURL: agentSrv.URL, Username: "u", Password: "p", Directory: sess.agent.directory, Timeout: time.Second})
	update := &models.Update{Message: &models.Message{
		Text: "/bogus", From: &models.User{ID: 7}, Chat: models.Chat{ID: 7},
	}}
	a.answer(context.Background(), b, update)
	if agentHits.Load() != 0 {
		t.Errorf("unknown command reached the agent %d times", agentHits.Load())
	}
	if reply != unknownCommandReply {
		t.Errorf("reply = %q, want %q", reply, unknownCommandReply)
	}
}
