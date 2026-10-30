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
	// bootstrap marks a session that has never been prompted. The next
	// Ask injects the memory snapshot once, then clears it. Reset and the
	// model paths create sessions without prompting, so creation alone
	// cannot imply the snapshot was delivered.
	bootstrap bool
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

// createBody builds the session-create payload. The location pins the
// session's working directory to the workspace. The ?directory= query param
// alone does not do this: it only scopes listings, so without location every
// session lands in the server's own directory.
func (a *Agent) createBody() map[string]any {
	body := map[string]any{
		"title":    "telegram",
		"location": map[string]any{"directory": a.directory},
	}
	if a.defaultModel != nil {
		body["model"] = a.defaultModel.payload()
	}
	return body
}

func (a *Agent) create(ctx context.Context) (string, error) {
	body := a.createBody()
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
	a.bootstrap = true
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

// okReply is the whole reply when a finished turn produced no text, e.g.
// one that only ran tools. Tool internals stay out of chat by design.
const okReply = "👍️️"

// extractAnswer returns the newest completed assistant reply carrying text
// and whether one exists. Textless rounds (tool calls still running) report
// false so the waiter keeps waiting for the turn to end instead of answering
// early. Pure over fetched messages so tests pin it without a server.
func extractAnswer(messages []sessionMessage) (string, bool) {
	var best *sessionMessage
	var bestText string
	for i := range messages {
		m := &messages[i]
		if m.Type != "assistant" || m.Time.Completed == nil {
			continue
		}
		if best != nil && m.Time.Created <= best.Time.Created {
			continue
		}
		text, ok := messageText(m)
		if !ok {
			continue
		}
		best, bestText = m, text
	}
	if best == nil {
		return "", false
	}
	return bestText, true
}

// messageText joins one message's text parts and reports whether any
// survived trimming.
func messageText(m *sessionMessage) (string, bool) {
	var out bytes.Buffer
	for _, c := range m.Content {
		if c.Type == "text" {
			out.WriteString(c.Text + "\n")
		}
	}
	text := bytes.TrimSpace(out.Bytes())
	if len(text) == 0 {
		return "", false
	}
	return string(text), true
}

// turnOver reports whether the turn ended. The server appends an idle
// marker once the turn settles, after the last assistant message. Without
// it a poll landing between tool rounds would mistake an intermediate
// textless message for the answer.
func turnOver(fresh []sessionMessage) bool {
	for i := range fresh {
		if fresh[i].Type == "idle" {
			return true
		}
	}
	return false
}

// ProgressFunc receives growing partial reply text while the turn runs.
// It fires only when the newest assistant text changes, so callers can
// forward each value to a streaming preview. A nil ProgressFunc disables
// progress reports without changing the wait.
type ProgressFunc func(partial string)

// latestPartial returns the newest assistant text whether or not its turn
// completed, so a slow turn can stream something before the final answer
// exists. Pure over fetched messages so tests pin it without a server.
func latestPartial(messages []sessionMessage) (string, bool) {
	var best *sessionMessage
	var bestText string
	for i := range messages {
		m := &messages[i]
		if m.Type != "assistant" {
			continue
		}
		if best != nil && m.Time.Created <= best.Time.Created {
			continue
		}
		text, ok := messageText(m)
		if !ok {
			continue
		}
		best, bestText = m, text
	}
	if best == nil {
		return "", false
	}
	return bestText, true
}

func (a *Agent) waitForAnswer(ctx context.Context, id string, since int64, onProgress ProgressFunc) (string, error) {
	deadline := time.Now().Add(a.timeout)
	var last string
	for {
		status, raw, err := a.call(ctx, http.MethodGet, "/session/"+url.PathEscape(id)+"/message", nil)
		if err == nil && status == http.StatusOK {
			var out envelope[[]sessionMessage]
			if err := json.Unmarshal(raw, &out); err == nil {
				var fresh, assistants []sessionMessage
				for _, m := range out.Data {
					if m.Time.Created < since {
						continue
					}
					fresh = append(fresh, m)
					if m.Type == "assistant" {
						assistants = append(assistants, m)
					}
				}
				if answer, ok := extractAnswer(assistants); ok {
					return answer, nil
				}
				if onProgress != nil {
					if partial, ok := latestPartial(assistants); ok && partial != last {
						last = partial
						onProgress(partial)
					}
				}
				if turnOver(fresh) {
					return okReply, nil
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
	return a.AskWithProgress(ctx, question, nil)
}

// AskWithProgress sends one question and reports growing partial text on
// onProgress while the turn runs. The returned reply is still the whole
// final answer.
func (a *Agent) AskWithProgress(ctx context.Context, question string, onProgress ProgressFunc) (string, error) {
	a.mu <- struct{}{}
	defer func() { <-a.mu }()

	fresh := false
	if a.sessionID == "" || !a.alive(ctx, a.sessionID) {
		id, err := a.create(ctx)
		if err != nil {
			return "", err
		}
		a.sessionID = id
		fresh = true
	}
	// A fresh session gets the memory snapshot ahead of the question. This
	// is the bootstrap injection: curated facts enter context once, and the
	// recall line rides along even for empty workspaces, so every session
	// learns both recall commands.
	text := question
	if fresh || a.bootstrap {
		text = withSnapshot(a.directory, question)
		a.bootstrap = false
	}
	since := time.Now().UnixMilli()
	status, err := a.prompt(ctx, a.sessionID, text)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		id, err := a.create(ctx)
		if err != nil {
			return "", err
		}
		a.sessionID = id
		if status, err := a.prompt(ctx, a.sessionID, withSnapshot(a.directory, question)); err != nil || status != http.StatusOK {
			return "", fmt.Errorf("prompt failed with %d", status)
		}
	} else if status != http.StatusOK {
		return "", fmt.Errorf("prompt failed with %d", status)
	}
	return a.waitForAnswer(ctx, a.sessionID, since, onProgress)
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
	Limit    struct {
		Context int64 `json:"context"`
	} `json:"limit"`
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

// sessionUsage is everything /usage reports. Limit is the model's context
// window from the catalog. Left is Limit minus used, floored at zero.
type sessionUsage struct {
	Input    int64
	Output   int64
	ModelID  string
	provider string
	Variant  string
	HasModel bool
	Limit    int64
	HasLimit bool
}

// used counts input plus output. Both occupy the window going forward,
// so their sum is what the context-left figure subtracts.
func (u sessionUsage) used() int64 { return u.Input + u.Output }

func (u sessionUsage) left() int64 {
	if left := u.Limit - u.used(); left > 0 {
		return left
	}
	return 0
}

// parseSessionUsage reads the session-get body into counters and model
// identity. The context limit joins later from the catalog. Pure over the
// body so tests pin it without a server.
func parseSessionUsage(raw []byte) (sessionUsage, error) {
	var out envelope[struct {
		Model *struct {
			ID       string `json:"id"`
			Provider string `json:"providerID"`
			Variant  string `json:"variant"`
		} `json:"model"`
		Tokens struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"tokens"`
	}]
	if err := json.Unmarshal(raw, &out); err != nil {
		return sessionUsage{}, err
	}
	u := sessionUsage{Input: out.Data.Tokens.Input, Output: out.Data.Tokens.Output}
	if m := out.Data.Model; m != nil {
		u.HasModel = true
		u.ModelID, u.provider, u.Variant = m.ID, m.Provider, m.Variant
	}
	return u, nil
}

// modelLimit finds the context window for one catalog model. Zero and
// missing limits report false so callers omit the context line instead of
// printing a bogus window.
func modelLimit(catalog []catalogEntry, provider, id string) (int64, bool) {
	for _, e := range catalog {
		if e.Provider == provider && e.ID == id && e.Limit.Context > 0 {
			return e.Limit.Context, true
		}
	}
	return 0, false
}

// Usage reports the live session's token counters and, when the session
// model is in the catalog, its context window. A catalog miss still
// returns the counters without a limit instead of failing the command.
func (a *Agent) Usage(ctx context.Context) (sessionUsage, error) {
	a.mu <- struct{}{}
	defer func() { <-a.mu }()

	if a.sessionID == "" || !a.alive(ctx, a.sessionID) {
		id, err := a.create(ctx)
		if err != nil {
			return sessionUsage{}, err
		}
		a.sessionID = id
		a.overridden = false
	}
	status, raw, err := a.call(ctx, http.MethodGet, "/session/"+url.PathEscape(a.sessionID), nil)
	if err != nil {
		return sessionUsage{}, err
	}
	if status != http.StatusOK {
		return sessionUsage{}, fmt.Errorf("session get failed with %d", status)
	}
	u, err := parseSessionUsage(raw)
	if err != nil {
		return sessionUsage{}, err
	}
	if !u.HasModel {
		return u, nil
	}
	catalog, err := a.Catalog(ctx)
	if err != nil {
		return u, nil
	}
	if limit, ok := modelLimit(catalog, u.provider, u.ModelID); ok {
		u.Limit, u.HasLimit = limit, true
	}
	return u, nil
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
