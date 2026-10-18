package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startSupervised boots a supervisor-managed server for the test and reaps
// it afterwards. No hand-started server is needed.
func startSupervised(t *testing.T) *ServeSupervisor {
	t.Helper()
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode binary not in PATH")
	}
	sup, err := StartServe(context.Background(), ServeOptions{RestartDelay: time.Second})
	if err != nil {
		t.Fatalf("start managed serve: %v", err)
	}
	t.Cleanup(sup.Shutdown)
	return sup
}

func supervisedAgent(t *testing.T, sup *ServeSupervisor, dir string) *Agent {
	t.Helper()
	return NewAgent(AgentOptions{
		BaseURL:   sup.BaseURL(),
		Username:  serveUsername,
		Password:  sup.Password(),
		Directory: dir,
		Timeout:   2 * time.Minute,
	})
}

// TestAgentLiveServe exercises Reset and Ask against a supervisor-managed
// server. Ask still needs the usual `opencode auth login` credentials.
func TestAgentLiveServe(t *testing.T) {
	sup := startSupervised(t)
	ctx := context.Background()
	a := supervisedAgent(t, sup, t.TempDir())
	if err := a.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	answer, err := a.Ask(ctx, "reply with exactly: hello mars")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if answer != "hello mars" {
		t.Fatalf("answer = %q", answer)
	}
	if err := a.Reset(ctx); err != nil {
		t.Fatalf("second reset: %v", err)
	}
}

// TestAgentLiveSessionLocation proves Reset pins the session to the
// workspace dir. Sessions created without a location land in the server's
// own directory, so the agent would not recognize its workspace.
func TestAgentLiveSessionLocation(t *testing.T) {
	sup := startSupervised(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := supervisedAgent(t, sup, dir)
	if err := a.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	status, raw, err := a.call(ctx, http.MethodGet, "/session", nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("list: status %d err %v", status, err)
	}
	var out envelope[[]struct {
		Title    string `json:"title"`
		Location *struct {
			Directory string `json:"directory"`
		} `json:"location"`
	}]
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range out.Data {
		if s.Title != "telegram" {
			continue
		}
		if s.Location == nil {
			t.Fatal("telegram session has no location; it runs in the server dir")
		}
		got, err := filepath.EvalSymlinks(s.Location.Directory)
		if err != nil {
			got = s.Location.Directory
		}
		if got != dir {
			t.Fatalf("session directory = %q, want %q", s.Location.Directory, dir)
		}
		return
	}
	t.Fatal("telegram session not listed under the workspace dir")
}

// TestAgentLiveUsage reads counters and the context window from a fresh
// supervisor-managed session without prompting, so it costs no model call.
func TestAgentLiveUsage(t *testing.T) {
	sup := startSupervised(t)
	ctx := context.Background()
	a := supervisedAgent(t, sup, t.TempDir())
	catalog, err := a.Catalog(ctx)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	var ref *ModelRef
	for _, e := range catalog {
		if e.Provider == zenProvider && e.Limit.Context > 0 {
			r := ModelRef{Provider: e.Provider, ID: e.ID}
			ref = &r
			break
		}
	}
	if ref == nil {
		t.Skip("no Zen model with a context limit in the catalog")
	}
	a.defaultModel = ref
	if err := a.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	u, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !u.HasModel || u.ModelID != ref.ID {
		t.Fatalf("model identity lost: %+v", u)
	}
	if !u.HasLimit || u.Limit <= 0 {
		t.Fatalf("context limit missing: %+v", u)
	}
	if u.Input != 0 || u.Output != 0 {
		t.Fatalf("fresh session must read zero: %+v", u)
	}
	if u.left() != u.Limit-u.used() {
		t.Fatalf("left must equal limit minus used: %+v", u)
	}
}

// TestAgentLiveModel exercises the /model paths against a
// supervisor-managed server using OPENCODE_TEST_MODEL in
// provider/model#variant form.
func TestAgentLiveModel(t *testing.T) {
	raw := os.Getenv("OPENCODE_TEST_MODEL")
	if raw == "" {
		t.Skip("set OPENCODE_TEST_MODEL")
	}
	ref, err := ParseModelRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	sup := startSupervised(t)
	ctx := context.Background()
	newAgent := func() *Agent {
		a := supervisedAgent(t, sup, t.TempDir())
		a.defaultModel = &ref
		return a
	}
	a := newAgent()
	if err := a.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	current, err := a.CurrentModel(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if !current.HasModel || current.ID != ref.ID || current.Provider != ref.Provider ||
		current.Variant != ref.Variant || current.Overridden {
		t.Fatalf("default not applied at create: %+v", current)
	}
	other := ref
	other.Variant = ""
	if other.Variant == ref.Variant {
		other.ID = ref.ID + "-alt"
	}
	if err := a.SetModel(ctx, other); err != nil {
		t.Logf("override not verified live: %v", err)
		return
	}
	current, err = a.CurrentModel(ctx)
	if err != nil {
		t.Fatalf("current after set: %v", err)
	}
	if !current.HasModel || !current.Overridden || current.ID != other.ID {
		t.Fatalf("override not visible: %+v", current)
	}
	if err := a.SetModel(ctx, ModelRef{Provider: "opencode", ID: "deepseek-v4.1-flash-fre"}); err == nil {
		t.Fatal("bogus model accepted")
	} else if !strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("missing suggestions: %v", err)
	}
	if err := a.SetModel(ctx, ModelRef{Provider: ref.Provider, ID: ref.ID, Variant: "bogus-variant"}); err == nil {
		t.Fatal("bogus variant accepted")
	}
	catalog, err := a.Catalog(ctx)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	zen := map[string]bool{}
	for _, e := range catalog {
		if e.Provider == "opencode" {
			zen[e.ID] = true
		}
	}
	lines := strings.Split(formatCatalog(catalog), "\n")
	if len(lines) == 0 {
		t.Fatal("empty model list")
	}
	for _, line := range lines {
		id := line
		if i := strings.IndexByte(id, ' '); i >= 0 {
			id = id[:i]
		}
		if strings.Contains(id, "/") || !zen[id] {
			t.Fatalf("non-Zen or prefixed line in list: %q", line)
		}
	}
}
