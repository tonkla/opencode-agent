package main

import (
	"context"
	"sort"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Every slash command the bot knows lives here. Plain text is not a slash
// command; the message handler stays in main.go.

// registerCommands wires each slash command with the allowlist guard.
func (a *app) registerCommands(b *bot.Bot) {
	b.RegisterHandler(bot.HandlerTypeMessageText, "ping", bot.MatchTypeCommand, a.requireUser(a.ping))
	b.RegisterHandler(bot.HandlerTypeMessageText, "new", bot.MatchTypeCommand, a.requireUser(a.fresh))
	b.RegisterHandler(bot.HandlerTypeMessageText, "model", bot.MatchTypeCommand, a.requireUser(a.model))
}

func (a *app) model(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	arg := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/model"))
	arg = strings.TrimSpace(arg)
	if arg == "" {
		current, err := a.agent.CurrentModel(ctx)
		if err != nil {
			a.sendReply(ctx, b, chatID, "Could not read the session model. Is opencode serve running?")
			return
		}
		a.sendReply(ctx, b, chatID, describeModel(current))
		return
	}
	if arg == "list" {
		catalog, err := a.agent.Catalog(ctx)
		if err != nil {
			a.sendReply(ctx, b, chatID, "Could not read the model catalog. Is opencode serve running?")
			return
		}
		a.sendReply(ctx, b, chatID, formatCatalog(catalog))
		return
	}
	ref, err := ParseModelRef(arg)
	if err != nil {
		a.sendReply(ctx, b, chatID, err.Error()+suggestSuffix(ctx, a.agent, arg))
		return
	}
	a.typing(ctx, b, chatID)
	if err := a.agent.SetModel(ctx, ref); err != nil {
		a.sendReply(ctx, b, chatID, "Could not switch model: "+err.Error())
		return
	}
	a.sendReply(ctx, b, chatID, "Model set to "+ref.String()+" for this session.")
}

// formatCatalog lists selectable Zen models with their variants, one per
// line, sorted for stable output. Pure over catalog entries so tests pin it.
func formatCatalog(catalog []catalogEntry) string {
	var lines []string
	for _, e := range catalog {
		if e.Provider != zenProvider {
			continue
		}
		line := e.ID
		var variants []string
		for _, v := range e.Variants {
			variants = append(variants, v.ID)
		}
		if len(variants) > 0 {
			line += " [" + strings.Join(variants, ", ") + "]"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "No Zen models available."
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func describeModel(m sessionModel) string {
	if !m.HasModel {
		return "No model override. The session uses the server default."
	}
	out := "Model " + m.ID
	if m.Variant != "" {
		out += "#" + m.Variant
	}
	if m.Overridden {
		out += " (session override)"
	} else {
		out += " (default)"
	}
	return out + "."
}

func (a *app) ping(ctx context.Context, b *bot.Bot, update *models.Update) {
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "pong"})
}

func (a *app) fresh(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	a.typing(ctx, b, chatID)
	if err := a.agent.Reset(ctx); err != nil {
		a.sendReply(ctx, b, chatID, "Could not start a fresh session. Is opencode serve running?")
		return
	}
	a.sendReply(ctx, b, chatID, "Fresh session started.")
}
