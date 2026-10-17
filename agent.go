package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Agent holds the one live OpenCode session. The zero sessionID means no
// session exists yet. All methods recreate a dead session transparently.
type Agent struct {
	client    *http.Client
	baseURL   string
	auth      string
	directory string
	timeout   time.Duration
	mu        chan struct{}
	sessionID string
}

// AgentOptions names the connection. Pass values, not positionals.
type AgentOptions struct {
	BaseURL   string
	Username  string
	Password  string
	Directory string
	Timeout   time.Duration
}

type envelope[T any] struct {
	Data T `json:"data"`
}

type messageTime struct {
	Created   int64  `json:"created"`
	Completed *int64 `json:"completed"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type sessionMessage struct {
	Type    string        `json:"type"`
	Time    messageTime   `json:"time"`
	Content []contentPart `json:"content"`
}

const pollInterval = 2 * time.Second

// NewAgent builds an Agent. The mutex channel serializes Telegram updates so
// two messages never interleave prompts on the one session.
func NewAgent(opts AgentOptions) *Agent {
	return &Agent{
		client:    &http.Client{Timeout: 30 * time.Second},
		baseURL:   opts.BaseURL,
		auth:      "Basic " + base64.StdEncoding.EncodeToString([]byte(opts.Username+":"+opts.Password)),
		directory: opts.Directory,
		timeout:   opts.Timeout,
		mu:        make(chan struct{}, 1),
	}
}

func (a *Agent) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+"/api"+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", a.auth)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	q := req.URL.Query()
	q.Set("directory", a.directory)
	req.URL.RawQuery = q.Encode()

	res, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, raw, nil
}

func (a *Agent) create(ctx context.Context) (string, error) {
	status, raw, err := a.call(ctx, http.MethodPost, "/session", map[string]string{"title": "telegram"})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("session create failed with %d", status)
	}
	var out envelope[struct {
		ID string `json:"id"`
	}]
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Data.ID, nil
}

// destroy is best effort. The old session is dropped either way once the new
// one exists.
func (a *Agent) destroy(ctx context.Context, id string) {
	a.call(ctx, http.MethodDelete, "/session/"+url.PathEscape(id), nil)
}

func (a *Agent) alive(ctx context.Context, id string) bool {
	status, _, err := a.call(ctx, http.MethodGet, "/session/"+url.PathEscape(id), nil)
	return err == nil && status == http.StatusOK
}

func (a *Agent) prompt(ctx context.Context, id, text string) (int, error) {
	status, _, err := a.call(ctx, http.MethodPost, "/session/"+url.PathEscape(id)+"/prompt",
		map[string]string{"text": text})
	return status, err
}

// extractAnswer returns the latest completed assistant reply and whether one
// exists. Pure over fetched messages so tests pin it without a server.
func extractAnswer(messages []sessionMessage) (string, bool) {
	var last *sessionMessage
	for i := range messages {
		m := &messages[i]
		if m.Type == "assistant" && m.Time.Completed != nil {
			last = m
		}
	}
	if last == nil {
		return "", false
	}
	var out bytes.Buffer
	for _, c := range last.Content {
		if c.Type == "text" {
			out.WriteString(c.Text + "\n")
		}
	}
	text := bytes.TrimSpace(out.Bytes())
	if len(text) == 0 {
		return "(empty reply)", true
	}
	return string(text), true
}

func (a *Agent) waitForAnswer(ctx context.Context, id string, since int64) (string, error) {
	deadline := time.Now().Add(a.timeout)
	for {
		status, raw, err := a.call(ctx, http.MethodGet, "/session/"+url.PathEscape(id)+"/message", nil)
		if err == nil && status == http.StatusOK {
			var out envelope[[]sessionMessage]
			if err := json.Unmarshal(raw, &out); err == nil {
				var fresh []sessionMessage
				for _, m := range out.Data {
					if m.Type == "assistant" && m.Time.Created >= since {
						fresh = append(fresh, m)
					}
				}
				if answer, ok := extractAnswer(fresh); ok {
					return answer, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("agent timed out")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Ask sends one question to the live session and waits for its reply.
func (a *Agent) Ask(ctx context.Context, question string) (string, error) {
	a.mu <- struct{}{}
	defer func() { <-a.mu }()

	if a.sessionID == "" || !a.alive(ctx, a.sessionID) {
		id, err := a.create(ctx)
		if err != nil {
			return "", err
		}
		a.sessionID = id
	}
	since := time.Now().UnixMilli()
	status, err := a.prompt(ctx, a.sessionID, question)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		id, err := a.create(ctx)
		if err != nil {
			return "", err
		}
		a.sessionID = id
		if status, err := a.prompt(ctx, a.sessionID, question); err != nil || status != http.StatusOK {
			return "", fmt.Errorf("prompt failed with %d", status)
		}
	} else if status != http.StatusOK {
		return "", fmt.Errorf("prompt failed with %d", status)
	}
	return a.waitForAnswer(ctx, a.sessionID, since)
}

// Reset drops the live session and starts a blank one.
func (a *Agent) Reset(ctx context.Context) error {
	a.mu <- struct{}{}
	defer func() { <-a.mu }()

	if a.sessionID != "" {
		a.destroy(ctx, a.sessionID)
	}
	id, err := a.create(ctx)
	if err != nil {
		return err
	}
	a.sessionID = id
	return nil
}
