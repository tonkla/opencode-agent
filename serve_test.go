package main

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGeneratePassword(t *testing.T) {
	a, err := generatePassword()
	if err != nil {
		t.Fatal(err)
	}
	b, err := generatePassword()
	if err != nil {
		t.Fatal(err)
	}
	if a == "" || a == b {
		t.Errorf("passwords must be non-empty and unique, got %q and %q", a, b)
	}
	for _, c := range a {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", c) {
			t.Errorf("password %q has non-URL-safe rune %q", a, c)
		}
	}
}

func TestFreePortBinds(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Errorf("free port %d did not rebind: %v", port, err)
		return
	}
	_ = l.Close()
}

func TestStartServeRejectsMissingBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := StartServe(ctx, ServeOptions{Binary: "/nonexistent/opencode-test-binary"}); err == nil {
		t.Error("expected error for missing binary")
	}
}

// stubSupervisor wires a supervisor at a test HTTP server. The server only
// answers /api/model with the right basic-auth pair.
func stubSupervisor(t *testing.T, password string, authed http.HandlerFunc) (*ServeSupervisor, *http.Server) {
	t.Helper()
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: authed}
	l, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &ServeSupervisor{
		password:     password,
		baseURL:      "http://" + l.Addr().String(),
		client:       &http.Client{Timeout: 2 * time.Second},
		readyTimeout: 3 * time.Second,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}, srv
}

func authStub(wantPass string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != serveUsername || pass != wantPass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/model" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func TestWaitReadyAcceptsGoodAuth(t *testing.T) {
	s, _ := stubSupervisor(t, "pw", authStub("pw"))
	exited := make(chan error)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.waitReady(ctx, exited); err != nil {
		t.Errorf("ready server with good auth: %v", err)
	}
}

func TestWaitReadyRejectsBadAuth(t *testing.T) {
	s, _ := stubSupervisor(t, "wrong", authStub("pw"))
	s.readyTimeout = time.Second
	exited := make(chan error)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.waitReady(ctx, exited); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("wrong password must time out not-ready, got %v", err)
	}
}

func TestWaitReadyFailsFastWhenChildDies(t *testing.T) {
	neverReady := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	s, _ := stubSupervisor(t, "pw", neverReady)
	exited := make(chan error, 1)
	exited <- context.Canceled
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.waitReady(ctx, exited); err == nil || !strings.Contains(err.Error(), "died while starting") {
		t.Errorf("dead child must fail fast, got %v", err)
	}
}

// TestSupervisorRestartsDeadServer kills the real managed child and proves
// the supervisor brings it back on the same address.
func TestSupervisorRestartsDeadServer(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode binary not in PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sup, err := StartServe(ctx, ServeOptions{RestartDelay: time.Second, ReadyTimeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sup.Shutdown()

	waitUp := func(what string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			if sup.probe(ctx) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("server never answered %s", what)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	waitUp("after start")
	first := sup.PID()
	if first == 0 {
		t.Fatal("no child pid after start")
	}

	if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	waitUp("after SIGKILL")
	if got := sup.PID(); got == 0 || got == first {
		t.Fatalf("pid after restart = %d, want a new live pid (was %d)", got, first)
	}
	if sup.BaseURL() == "" || sup.Password() == "" {
		t.Error("supervisor must keep its address and password across restarts")
	}
}
