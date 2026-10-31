package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const maxReplyRunes = 4000

// draftTextLimit caps a streaming preview below the Telegram cap, leaving
// room for the cursor the progress callback appends.
const draftTextLimit = telegramLimit - 16

// userSession is one Telegram user's isolated state: its own OpenCode
// session via its own Agent, its own history file, and its own workspace
// directory holding every *.md, history/, and memory/ artifact.
type userSession struct {
	agent   *Agent
	history *chatHistory
}

type app struct {
	config       Config
	baseDir      string
	baseURL      string
	password     string
	timeout      time.Duration
	defaultModel *ModelRef

	mu       sync.Mutex
	sessions map[int64]*userSession
}

// isAllowed reports whether id is in the allowlist.
func (a *app) isAllowed(id int64) bool {
	for _, want := range a.config.AllowedUserIDs {
		if id == want {
			return true
		}
	}
	return false
}

// userDir resolves one user's workspace: baseDir/<user_id>.
func userDir(base string, userID int64) string {
	return filepath.Join(base, strconv.FormatInt(userID, 10))
}

// sessionFor returns the live session for userID, creating its workspace
// directory, Agent, and history on first use.
func (a *app) sessionFor(userID int64) (*userSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[userID]; ok {
		return s, nil
	}
	dir := userDir(a.baseDir, userID)
	if err := ensureWorkspaceDir(dir); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "opencode-agent"
	}
	if err := SeedRecallCommands(dir, exe); err != nil {
		log.Printf("user %d recall commands: %v", userID, err)
	}
	opts := AgentOptions{
		BaseURL:   a.baseURL,
		Username:  serveUsername,
		Password:  a.password,
		Directory: dir,
		Timeout:   a.timeout,
	}
	if a.defaultModel != nil {
		opts.Model = *a.defaultModel
		opts.HasModel = true
	}
	s := &userSession{
		agent:   NewAgent(opts),
		history: newChatHistory(dir),
	}
	if a.sessions == nil {
		a.sessions = map[int64]*userSession{}
	}
	a.sessions[userID] = s
	return s, nil
}

func (a *app) requireUser(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil || update.Message.From == nil || !a.isAllowed(update.Message.From.ID) {
			return
		}
		sess, err := a.sessionFor(update.Message.From.ID)
		if err != nil {
			log.Printf("user %d workspace: %v", update.Message.From.ID, err)
			return
		}
		if !skipsHistory(update.Message.Text) {
			sess.history.appendUser(update.Message.Text)
		}
		next(ctx, b, update)
	}
}

// skipsHistory reports any slash command invocation: known or unknown,
// with an optional @bot suffix and arguments. Every slash round trip
// stays out of history: session mechanics, not conversation.
func skipsHistory(text string) bool {
	_, ok := commandName(text)
	return ok
}

// isNewCommand reports a /new invocation, with an optional @bot suffix.
// The /new round trip stays out of history, so it needs no exact match
// against the reply text.
func isNewCommand(text string) bool {
	rest, ok := strings.CutPrefix(text, "/new")
	return ok && (rest == "" || rest[0] == ' ' || rest[0] == '@')
}

// isStartCommand reports a /start invocation, with an optional @bot
// suffix. /start is an alias of /new and stays out of history like it.
func isStartCommand(text string) bool {
	rest, ok := strings.CutPrefix(text, "/start")
	return ok && (rest == "" || rest[0] == ' ' || rest[0] == '@')
}

// isHelpCommand reports a /help invocation, with an optional @bot
// suffix. The listing stays out of history like /start does.
func isHelpCommand(text string) bool {
	rest, ok := strings.CutPrefix(text, "/help")
	return ok && (rest == "" || rest[0] == ' ' || rest[0] == '@')
}

// scrubPaths redacts server-side filesystem paths from chat-bound
// text. Tool results surface absolute paths and the model echoes them,
// so redact at the send boundary instead of trusting the model to omit
// them. Pure over its inputs so tests pin it without a server.
func scrubPaths(baseDir, dir, text string) string {
	if dir != "" && dir != baseDir {
		text = strings.ReplaceAll(text, dir, "~")
	}
	if baseDir != "" {
		text = strings.ReplaceAll(text, baseDir, "~")
	}
	return text
}

func (a *app) sendReply(ctx context.Context, b *bot.Bot, chatID, userID int64, text string) {
	text = scrubPaths(a.baseDir, userDir(a.baseDir, userID), text)
	if sess, err := a.sessionFor(userID); err == nil {
		sess.history.appendAgent(text)
	} else {
		log.Printf("user %d history: %v", userID, err)
	}
	a.sendChunks(ctx, b, chatID, text)
}

// sendReplySilent delivers one reply without persisting it, for every
// slash command round trip. Slash commands are session mechanics, not
// conversation, so neither the invocation nor its reply reaches history.
func (a *app) sendReplySilent(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	a.sendChunks(ctx, b, chatID, text)
}

// sendDraft streams one ephemeral preview as Telegram HTML. Failures
// stay silent: the preview is cosmetic and the final sendMessage below
// must still run. Empty text shows the client's thinking placeholder.
func (a *app) sendDraft(ctx context.Context, b *bot.Bot, chatID int64, draftID, text string) {
	_, _ = b.SendMessageDraft(ctx, &bot.SendMessageDraftParams{ChatID: chatID, DraftID: draftID, Text: telegramHTML(text), ParseMode: models.ParseModeHTML})
}

func (a *app) sendChunks(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	for _, piece := range splitMarkdown(text, telegramLimit) {
		html := telegramHTML(piece)
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: html, ParseMode: models.ParseModeHTML}); err != nil && isUnparseable(err) {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: piece})
		}
	}
}

func (a *app) typing(ctx context.Context, b *bot.Bot, chatID int64) {
	_, _ = b.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
}

// typingInterval re-sends the indicator faster than its server-side
// lifetime of about five seconds, so slow turns never go quiet. A var so
// tests can shrink it.
var typingInterval = 4 * time.Second

// keepTyping holds the typing indicator for a slow turn and returns its
// stop. Callers stop it when the wait ends. A cancelled parent stops it
// too, so the goroutine cannot leak.
func (a *app) keepTyping(ctx context.Context, b *bot.Bot, chatID int64) context.CancelFunc {
	live, stop := context.WithCancel(ctx)
	a.typing(live, b, chatID)
	go func() {
		t := time.NewTicker(typingInterval)
		defer t.Stop()
		for {
			select {
			case <-live.Done():
				return
			case <-t.C:
				a.typing(live, b, chatID)
			}
		}
	}()
	return stop
}

func (a *app) answer(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID
	if isUnknownCommand(update.Message.Text) {
		a.sendReplySilent(ctx, b, chatID, unknownCommandReply)
		return
	}
	sess, err := a.sessionFor(userID)
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not open your workspace; try again in a moment.")
		return
	}
	stop := a.keepTyping(ctx, b, chatID)
	var onProgress ProgressFunc
	if update.Message.Chat.Type == models.ChatTypePrivate {
		draftID := strconv.FormatInt(time.Now().UnixNano(), 10)
		a.sendDraft(ctx, b, chatID, draftID, "")
		last := ""
		onProgress = func(partial string) {
			scrubbed := scrubPaths(a.baseDir, userDir(a.baseDir, userID), partial)
			pieces := splitMarkdown(scrubbed, draftTextLimit)
			if len(pieces) == 0 {
				return
			}
			display := pieces[0] + " ▌"
			if display == last {
				return
			}
			last = display
			a.sendDraft(ctx, b, chatID, draftID, display)
		}
	}
	reply, err := sess.agent.AskWithProgress(ctx, update.Message.Text, onProgress)
	stop()
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "The agent request failed. The local server may be restarting; try again in a moment.")
		return
	}
	a.sendReply(ctx, b, chatID, userID, reply)
}

// ensureWorkspaceDir creates dir and verifies the agent can write to it.
// MkdirAll alone succeeds on an existing read-only dir, so probe with a
// temp file to fail fast instead of mid-session.
func ensureWorkspaceDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("agent_workspace_dir %q is not writable: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("agent_workspace_dir %q probe cleanup failed: %w", dir, err)
	}
	return nil
}

// resolveWorkspaceDir returns dir as an absolute path. The serve API
// rejects a relative session location with a 500, so resolve before the
// directory reaches any session-create payload or query param.
func resolveWorkspaceDir(dir string) (string, error) {
	return filepath.Abs(dir)
}

func run(ctx context.Context) error {
	if len(os.Args) > 1 && os.Args[1] == "recall" {
		return runRecall(os.Args[2:], os.Stdout)
	}
	configPath := flag.String("config", "config.toml", "path to config.toml")
	flag.Parse()
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return err
	}
	cfg.WorkspaceDir, err = resolveWorkspaceDir(cfg.WorkspaceDir)
	if err != nil {
		return err
	}
	if err := ensureWorkspaceDir(cfg.WorkspaceDir); err != nil {
		return err
	}

	sup, err := StartServe(ctx, ServeOptions{})
	if err != nil {
		return fmt.Errorf("local opencode server failed to start: %w", err)
	}
	defer sup.Shutdown()

	probe := NewAgent(AgentOptions{
		BaseURL:   sup.BaseURL(),
		Username:  serveUsername,
		Password:  sup.Password(),
		Directory: cfg.WorkspaceDir,
		Timeout:   time.Duration(cfg.AgentTimeoutMs) * time.Millisecond,
	})
	text := cfg.Model
	if cfg.ModelVariant != "" {
		text += "#" + cfg.ModelVariant
	}
	ref, err := ParseModelRef(text)
	if err != nil {
		return fmt.Errorf("bad model in config.toml: %w%s", err, suggestSuffix(ctx, probe, text))
	}
	probe.defaultModel = &ref
	if err := probe.Reset(ctx); err != nil {
		return fmt.Errorf("local opencode server is not responding: %w", err)
	}
	if err := probe.ValidateModel(ctx, ref); err != nil {
		return fmt.Errorf("bad default model in config.toml: %w", err)
	}

	// Drop updates no handler claims. Without this the library dumps every
	// such update with log.Printf.
	b, err := bot.New(cfg.BotToken, bot.WithDefaultHandler(func(context.Context, *bot.Bot, *models.Update) {}))
	if err != nil {
		return err
	}
	a := &app{
		config:       cfg,
		baseDir:      cfg.WorkspaceDir,
		baseURL:      sup.BaseURL(),
		password:     sup.Password(),
		timeout:      time.Duration(cfg.AgentTimeoutMs) * time.Millisecond,
		defaultModel: &ref,
		sessions:     map[int64]*userSession{},
	}
	for _, id := range cfg.AllowedUserIDs {
		if _, err := a.sessionFor(id); err != nil {
			return fmt.Errorf("user %d workspace: %w", id, err)
		}
	}
	a.registerCommands(b)
	a.publishCommands(ctx, b)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, a.requireUser(a.answer))
	b.Start(ctx)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
