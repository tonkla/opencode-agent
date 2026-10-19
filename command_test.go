package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
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
	want := []string{"start", "ping", "new", "model", "usage", "remember", "forget"}
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
	for _, name := range []string{"start", "ping", "new", "model", "usage", "remember", "forget"} {
		if !strings.Contains(body, `"command":"`+name+`"`) {
			t.Errorf("menu payload misses %q: %s", name, body)
		}
	}
}
