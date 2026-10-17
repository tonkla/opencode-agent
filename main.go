package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const maxReplyRunes = 4000

// splitMessage cuts long replies into Telegram-sized chunks.
func splitMessage(text string) []string {
	runes := []rune(text)
	var out []string
	for i := 0; i < len(runes); i += maxReplyRunes {
		end := i + maxReplyRunes
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

type app struct {
	agent  *Agent
	config Config
}

func (a *app) requireUser(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil || update.Message.From == nil || update.Message.From.ID != a.config.AllowedUserID {
			return
		}
		next(ctx, b, update)
	}
}

func (a *app) sendReply(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	for _, chunk := range splitMessage(text) {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: chunk})
	}
}

func (a *app) typing(ctx context.Context, b *bot.Bot, chatID int64) {
	_, _ = b.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
}

func (a *app) answer(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	a.typing(ctx, b, chatID)
	reply, err := a.agent.Ask(ctx, update.Message.Text)
	if err != nil {
		a.sendReply(ctx, b, chatID, "The agent request failed. Is opencode serve running?")
		return
	}
	a.sendReply(ctx, b, chatID, reply)
}

func run(ctx context.Context) error {
	configPath := flag.String("config", "config.toml", "path to config.toml")
	flag.Parse()
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		return err
	}

	agent := NewAgent(AgentOptions{
		BaseURL:   cfg.OpencodeBaseURL,
		Username:  cfg.OpencodeUser,
		Password:  cfg.OpencodePass,
		Directory: cfg.WorkspaceDir,
		Timeout:   time.Duration(cfg.AgentTimeoutMs) * time.Millisecond,
	})
	text := cfg.Model
	if cfg.ModelVariant != "" {
		text += "#" + cfg.ModelVariant
	}
	ref, err := ParseModelRef(text)
	if err != nil {
		return fmt.Errorf("bad model in config.toml: %w%s", err, suggestSuffix(ctx, agent, text))
	}
	agent.defaultModel = &ref
	if err := agent.Reset(ctx); err != nil {
		return fmt.Errorf("opencode serve unreachable: %w", err)
	}
	if err := agent.ValidateModel(ctx, *agent.defaultModel); err != nil {
		return fmt.Errorf("bad default model in config.toml: %w", err)
	}

	// Drop updates no handler claims. Without this the library dumps every
	// such update with log.Printf.
	b, err := bot.New(cfg.BotToken, bot.WithDefaultHandler(func(context.Context, *bot.Bot, *models.Update) {}))
	if err != nil {
		return err
	}
	a := &app{agent: agent, config: cfg}
	a.registerCommands(b)
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
