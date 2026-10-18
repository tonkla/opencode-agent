package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// serveUsername is the basic-auth user the opencode server expects. The
// server rejects any other name, so it is fixed here, not configured.
const serveUsername = "opencode"

const (
	defaultReadyTimeout = 30 * time.Second
	defaultRestartDelay = 2 * time.Second
	readyPollInterval   = 500 * time.Millisecond
	shutdownGrace       = 5 * time.Second
)

// ServeOptions tunes the managed server. Zero values pick the binary from
// PATH, a free loopback port, and a fresh random password.
type ServeOptions struct {
	Binary       string
	Port         int
	Password     string
	ReadyTimeout time.Duration
	RestartDelay time.Duration
}

// ServeSupervisor owns one opencode serve child. The port and password are
// chosen once at startup and reused across restarts, so the agent client
// never needs updating when the child dies.
type ServeSupervisor struct {
	binary       string
	port         int
	password     string
	baseURL      string
	client       *http.Client
	readyTimeout time.Duration
	restartDelay time.Duration

	mu  sync.Mutex
	cmd *exec.Cmd

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// generatePassword returns 32 random URL-safe characters for
// OPENCODE_PASSWORD. It never touches disk; both ends hold it in memory.
func generatePassword() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// freePort reserves a loopback port by binding :0, then releases it. The
// caller rebinds it immediately, so the race is one short localhost window.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// StartServe launches the child and blocks until it answers the model
// catalog. A slow or crashing binary fails here so the process exits and
// launchd retries, instead of serving Telegram with a dead backend.
func StartServe(ctx context.Context, opts ServeOptions) (*ServeSupervisor, error) {
	binary := opts.Binary
	if binary == "" {
		var err error
		binary, err = exec.LookPath("opencode")
		if err != nil {
			return nil, fmt.Errorf("opencode binary not found in PATH: install it and run `opencode auth login`")
		}
	}
	port := opts.Port
	if port == 0 {
		var err error
		port, err = freePort()
		if err != nil {
			return nil, fmt.Errorf("pick a serve port: %w", err)
		}
	}
	password := opts.Password
	if password == "" {
		var err error
		password, err = generatePassword()
		if err != nil {
			return nil, fmt.Errorf("generate serve password: %w", err)
		}
	}
	readyTimeout := opts.ReadyTimeout
	if readyTimeout == 0 {
		readyTimeout = defaultReadyTimeout
	}
	restartDelay := opts.RestartDelay
	if restartDelay == 0 {
		restartDelay = defaultRestartDelay
	}
	s := &ServeSupervisor{
		binary:       binary,
		port:         port,
		password:     password,
		baseURL:      "http://127.0.0.1:" + strconv.Itoa(port),
		client:       &http.Client{Timeout: 5 * time.Second},
		readyTimeout: readyTimeout,
		restartDelay: restartDelay,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
	firstReady := make(chan error, 1)
	go s.supervise(ctx, firstReady)
	select {
	case err := <-firstReady:
		if err != nil {
			s.Shutdown()
			return nil, err
		}
	case <-ctx.Done():
		s.Shutdown()
		return nil, ctx.Err()
	}
	log.Printf("opencode serve managed internally at %s", s.baseURL)
	return s, nil
}

// BaseURL is the loopback address the child listens on. It is fixed for the
// supervisor's lifetime, including across restarts.
func (s *ServeSupervisor) BaseURL() string { return s.baseURL }

// Password is the generated OPENCODE_PASSWORD the child was started with.
func (s *ServeSupervisor) Password() string { return s.password }

// PID reports the live child pid, or 0 when none is running.
func (s *ServeSupervisor) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Shutdown stops the loop and reaps the child. It is idempotent and returns
// only after the child is gone, so deferred calls never orphan the server.
func (s *ServeSupervisor) Shutdown() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	<-s.doneCh
}

func (s *ServeSupervisor) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

// supervise runs the restart loop until Shutdown or ctx. The first successful
// readiness probe reports on firstReady; a first start that never becomes
// ready reports its error and ends the loop so StartServe fails fast.
func (s *ServeSupervisor) supervise(ctx context.Context, firstReady chan<- error) {
	defer close(s.doneCh)
	first := true
	for {
		if s.stopped() || ctx.Err() != nil {
			return
		}
		cmd, err := s.start()
		if err != nil {
			if first {
				firstReady <- err
				return
			}
			log.Printf("opencode serve start failed: %v; retrying", err)
			if !s.sleepOrStop(ctx, s.restartDelay) {
				return
			}
			continue
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		if err := s.waitReady(ctx, exited); err != nil {
			if stopErr := s.terminate(cmd, exited); stopErr != nil {
				log.Printf("stop opencode serve: %v", stopErr)
			}
			if first {
				firstReady <- err
				return
			}
			log.Printf("opencode serve never became ready: %v; restarting", err)
			if !s.sleepOrStop(ctx, s.restartDelay) {
				return
			}
			continue
		}
		if first {
			firstReady <- nil
			first = false
		}
		select {
		case <-s.stopCh:
			if err := s.terminate(cmd, exited); err != nil {
				log.Printf("stop opencode serve: %v", err)
			}
			return
		case <-ctx.Done():
			if err := s.terminate(cmd, exited); err != nil {
				log.Printf("stop opencode serve: %v", err)
			}
			return
		case err := <-exited:
			log.Printf("opencode serve exited (%v); restarting", err)
			if !s.sleepOrStop(ctx, s.restartDelay) {
				return
			}
		}
	}
}

// start launches one child with the generated password in its environment.
// A stale OPENCODE_PASSWORD in our own env is filtered so the generated one
// always wins.
func (s *ServeSupervisor) start() (*exec.Cmd, error) {
	cmd := exec.Command(s.binary, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(s.port))
	env := os.Environ()
	kept := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "OPENCODE_PASSWORD=") {
			kept = append(kept, kv)
		}
	}
	cmd.Env = append(kept, "OPENCODE_PASSWORD="+s.password)
	cmd.Stdout = &logPrefix{tag: "serve stdout"}
	cmd.Stderr = &logPrefix{tag: "serve stderr"}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	return cmd, nil
}

// probe hits the model catalog with our credentials. A 200 means the child
// is up and accepting our auth.
func (s *ServeSupervisor) probe(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/model", nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth(serveUsername, s.password)
	res, err := s.client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, res.Body)
	if err := res.Body.Close(); err != nil {
		return false
	}
	return res.StatusCode == http.StatusOK
}

// waitReady polls the probe until it passes, the child exits early, or the
// timeout, stop, and ctx end the wait.
func (s *ServeSupervisor) waitReady(ctx context.Context, exited <-chan error) error {
	deadline := time.Now().Add(s.readyTimeout)
	t := time.NewTicker(readyPollInterval)
	defer t.Stop()
	for {
		if s.probe(ctx) {
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("serve died while starting: %w", err)
		case <-s.stopCh:
			return fmt.Errorf("stopped while waiting for serve")
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("serve not ready after %s", s.readyTimeout)
			}
		}
	}
}

// terminate asks the child to exit and kills it after a grace period. It
// reports how the child went away and always consumes the exited channel
// so no zombie remains.
func (s *ServeSupervisor) terminate(cmd *exec.Cmd, exited <-chan error) (err error) {
	if cmd.Process == nil {
		return nil
	}
	defer func() {
		s.mu.Lock()
		if s.cmd == cmd {
			s.cmd = nil
		}
		s.mu.Unlock()
	}()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case err := <-exited:
		return err
	case <-time.After(shutdownGrace):
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		return <-exited
	}
}

func (s *ServeSupervisor) sleepOrStop(ctx context.Context, d time.Duration) bool {
	select {
	case <-s.stopCh:
		return false
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// logPrefix forwards child output line by line to our own log, tagged so
// launchd logs show which side wrote each line.
type logPrefix struct {
	tag string
	buf []byte
}

func (w *logPrefix) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := -1
		for j, c := range w.buf {
			if c == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		log.Print("[" + w.tag + "] " + string(w.buf[:i]))
		w.buf = append([]byte(nil), w.buf[i+1:]...)
	}
	return len(p), nil
}
