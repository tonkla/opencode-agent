package main

import (
	"context"
	"os"
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
