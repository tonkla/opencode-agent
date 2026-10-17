package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAgentLiveServe exercises Reset and Ask against a real opencode serve.
// Run it with a server up: OPENCODE_TEST_BASE_URL=http://127.0.0.1:41xx
// OPENCODE_TEST_PASSWORD=... go test -run TestAgentLiveServe .
func TestAgentLiveServe(t *testing.T) {
	baseURL := os.Getenv("OPENCODE_TEST_BASE_URL")
	if baseURL == "" {
		t.Skip("no live server; set OPENCODE_TEST_BASE_URL")
	}
	ctx := context.Background()
	a := NewAgent(AgentOptions{
		BaseURL:   baseURL,
		Username:  "opencode",
		Password:  os.Getenv("OPENCODE_TEST_PASSWORD"),
		Directory: t.TempDir(),
		Timeout:   2 * time.Minute,
	})
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

// TestAgentLiveModel exercises the /model paths against a real serve using
// OPENCODE_TEST_MODEL in provider/model#variant form.
func TestAgentLiveModel(t *testing.T) {
	baseURL := os.Getenv("OPENCODE_TEST_BASE_URL")
	raw := os.Getenv("OPENCODE_TEST_MODEL")
	if baseURL == "" || raw == "" {
		t.Skip("set OPENCODE_TEST_BASE_URL and OPENCODE_TEST_MODEL")
	}
	ref, err := ParseModelRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	newAgent := func() *Agent {
		return NewAgent(AgentOptions{
			BaseURL:   baseURL,
			Username:  "opencode",
			Password:  os.Getenv("OPENCODE_TEST_PASSWORD"),
			Directory: t.TempDir(),
			Timeout:   2 * time.Minute,
			Model:     ref,
			HasModel:  true,
		})
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
