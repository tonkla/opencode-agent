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
	"strings"
	"time"
)

// Agent holds the one live OpenCode session. The zero sessionID means no
// session exists yet. All methods recreate a dead session transparently.
type Agent struct {
	client       *http.Client
	baseURL      string
	auth         string
	directory    string
	timeout      time.Duration
	mu           chan struct{}
	sessionID    string
	defaultModel *ModelRef
	overridden   bool
}

func (m ModelRef) payload() map[string]string {
	out := map[string]string{"providerID": m.Provider, "id": m.ID}
	if m.Variant != "" {
		out["variant"] = m.Variant
	}
	return out
}

// AgentOptions names the connection. Pass values, not positionals.
type AgentOptions struct {
	BaseURL   string
	Username  string
	Password  string
	Directory string
	Timeout   time.Duration
	Model     ModelRef
	HasModel  bool
}

// ModelRef names an OpenCode model in model#variant form with an optional
// provider prefix. The model ID passes through untouched, so ":free" and
// "-free" suffixes both survive parsing.
type ModelRef struct {
	Provider string
	ID       string
	Variant  string
}

// ParseModelRef splits "model#variant" with an optional "provider/" prefix.
// A missing provider defaults to the Zen provider since nothing else is
// selectable. A present provider ends at the first slash so IDs like
// "deepseek/deepseek-v4.1-flash" stay whole, and the variant starts at the
// last "#" so IDs containing "#" keep working.
func ParseModelRef(text string) (ModelRef, error) {
	rest := text
	var variant string
	if i := strings.LastIndexByte(rest, '#'); i >= 0 {
		variant = rest[i+1:]
		rest = rest[:i]
		if variant == "" {
			return ModelRef{}, fmt.Errorf("usage: /model model#variant")
		}
	}
	provider := zenProvider
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		if i == 0 || i == len(rest)-1 {
			return ModelRef{}, fmt.Errorf("usage: /model model#variant")
		}
		provider = rest[:i]
		rest = rest[i+1:]
	}
	if rest == "" {
		return ModelRef{}, fmt.Errorf("usage: /model model#variant")
	}
	return ModelRef{Provider: provider, ID: rest, Variant: variant}, nil
}

// String renders the short user-facing form. The provider is omitted since
// every selectable model shares it.
func (m ModelRef) String() string {
	if m.Variant != "" {
		return m.ID + "#" + m.Variant
	}
	return m.ID
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
	a := &Agent{
		client:    &http.Client{Timeout: 30 * time.Second},
		baseURL:   opts.BaseURL,
		auth:      "Basic " + base64.StdEncoding.EncodeToString([]byte(opts.Username+":"+opts.Password)),
		directory: opts.Directory,
		timeout:   opts.Timeout,
		mu:        make(chan struct{}, 1),
	}
	if opts.HasModel {
		m := opts.Model
		a.defaultModel = &m
	}
	return a
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
	body := map[string]any{"title": "telegram"}
	if a.defaultModel != nil {
		body["model"] = a.defaultModel.payload()
	}
	status, raw, err := a.call(ctx, http.MethodPost, "/session", body)
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
	a.overridden = false
	return nil
}

type catalogVariant struct {
	ID string `json:"id"`
}

type catalogEntry struct {
	Provider string           `json:"providerID"`
	ID       string           `json:"id"`
	Variants []catalogVariant `json:"variants"`
}

// zenProvider is the only model provider this agent may use. OpenCode Zen
// models are served under it. Everything else, including OpenRouter, is
// rejected before it can reach a session.
const zenProvider = "opencode"

// matchModel checks ref against the server catalog. IDs compare verbatim so
// ":free" and "-free" forms each match only themselves.
func matchModel(catalog []catalogEntry, ref ModelRef) error {
	for _, e := range catalog {
		if e.Provider != ref.Provider || e.ID != ref.ID {
			continue
		}
		if e.Provider != zenProvider {
			return fmt.Errorf("model %q is not an OpenCode Zen model", ref.Provider+"/"+ref.ID)
		}
		if ref.Variant == "" {
			return nil
		}
		var known []string
		for _, v := range e.Variants {
			known = append(known, v.ID)
			if v.ID == ref.Variant {
				return nil
			}
		}
		return fmt.Errorf("unknown variant %q for %s (available: %s)", ref.Variant, ref.String(), strings.Join(known, ", "))
	}
	err := fmt.Errorf("unknown model %q", ref.Provider+"/"+ref.ID)
	if sug := SuggestRefs(catalog, ref.ID, 5); len(sug) > 0 {
		err = fmt.Errorf("unknown model %q (did you mean: %s)", ref.Provider+"/"+ref.ID, strings.Join(sug, ", "))
	}
	return err
}

// suggestSuffix answers a bare model name with catalog matches. It returns ""
// when the token already names a provider or the catalog is unreachable, so
// callers stay correct on the unhappy path.
func suggestSuffix(ctx context.Context, a *Agent, token string) string {
	if i := strings.IndexByte(token, '/'); i >= 0 {
		token = token[i+1:]
	}
	catalog, err := a.Catalog(ctx)
	if err != nil {
		return ""
	}
	sug := SuggestRefs(catalog, token, 5)
	if len(sug) == 0 {
		return ""
	}
	return " (did you mean: " + strings.Join(sug, ", ") + ")"
}

// SuggestRefs returns up to n Zen model IDs whose ID contains the token,
// case-insensitively. Only Zen models are suggested since nothing else can
// be selected. It answers "what is this model actually called here".
func SuggestRefs(catalog []catalogEntry, token string, n int) []string {
	var out []string
	lower := strings.ToLower(token)
	for _, e := range catalog {
		if e.Provider != zenProvider {
			continue
		}
		if strings.Contains(strings.ToLower(e.ID), lower) {
			out = append(out, e.ID)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// ValidateModel rejects refs the server catalog does not carry.
func (a *Agent) ValidateModel(ctx context.Context, ref ModelRef) error {
	catalog, err := a.Catalog(ctx)
	if err != nil {
		return err
	}
	return matchModel(catalog, ref)
}

// Catalog fetches the server model list.
func (a *Agent) Catalog(ctx context.Context) ([]catalogEntry, error) {
	status, raw, err := a.call(ctx, http.MethodGet, "/model", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("model catalog failed with %d", status)
	}
	var out envelope[[]catalogEntry]
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

type sessionModel struct {
	ID         string `json:"id"`
	Provider   string `json:"providerID"`
	Variant    string `json:"variant"`
	HasModel   bool   `json:"-"`
	Overridden bool   `json:"-"`
}

// CurrentModel reports the session's model. HasModel is false when the
// session has none and falls back to the server default.
func (a *Agent) CurrentModel(ctx context.Context) (sessionModel, error) {
	a.mu <- struct{}{}
	defer func() { <-a.mu }()

	if a.sessionID == "" || !a.alive(ctx, a.sessionID) {
		id, err := a.create(ctx)
		if err != nil {
			return sessionModel{}, err
		}
		a.sessionID = id
		a.overridden = false
	}
	status, raw, err := a.call(ctx, http.MethodGet, "/session/"+url.PathEscape(a.sessionID), nil)
	if err != nil {
		return sessionModel{}, err
	}
	if status != http.StatusOK {
		return sessionModel{}, fmt.Errorf("session get failed with %d", status)
	}
	var out envelope[struct {
		Model *struct {
			ID       string `json:"id"`
			Provider string `json:"providerID"`
			Variant  string `json:"variant"`
		} `json:"model"`
	}]
	if err := json.Unmarshal(raw, &out); err != nil {
		return sessionModel{}, err
	}
	if out.Data.Model == nil {
		return sessionModel{Overridden: a.overridden}, nil
	}
	m := out.Data.Model
	return sessionModel{ID: m.ID, Provider: m.Provider, Variant: m.Variant, HasModel: true, Overridden: a.overridden}, nil
}

// SetModel switches the live session to ref until the session ends.
func (a *Agent) SetModel(ctx context.Context, ref ModelRef) error {
	a.mu <- struct{}{}
	defer func() { <-a.mu }()

	if a.sessionID == "" || !a.alive(ctx, a.sessionID) {
		id, err := a.create(ctx)
		if err != nil {
			return err
		}
		a.sessionID = id
	}
	if err := a.ValidateModel(ctx, ref); err != nil {
		return err
	}
	status, raw, err := a.call(ctx, http.MethodPost, "/session/"+url.PathEscape(a.sessionID)+"/model",
		map[string]any{"model": ref.payload()})
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("model switch failed with %d: %s", status, string(raw))
	}
	a.overridden = true
	return nil
}
