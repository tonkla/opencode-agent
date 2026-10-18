package main

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Every slash command the bot knows lives here. Plain text is not a slash
// command; the message handler stays in main.go.

// registerCommands wires each slash command with the allowlist guard.
func (a *app) registerCommands(b *bot.Bot) {
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, a.requireUser(a.start))
	b.RegisterHandler(bot.HandlerTypeMessageText, "ping", bot.MatchTypeCommand, a.requireUser(a.ping))
	b.RegisterHandler(bot.HandlerTypeMessageText, "new", bot.MatchTypeCommand, a.requireUser(a.fresh))
	b.RegisterHandler(bot.HandlerTypeMessageText, "model", bot.MatchTypeCommand, a.requireUser(a.model))
	b.RegisterHandler(bot.HandlerTypeMessageText, "usage", bot.MatchTypeCommand, a.requireUser(a.usage))
	b.RegisterHandler(bot.HandlerTypeMessageText, "remember", bot.MatchTypeCommand, a.requireUser(a.remember))
	b.RegisterHandler(bot.HandlerTypeMessageText, "forget", bot.MatchTypeCommand, a.requireUser(a.forget))
}

func (a *app) model(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	arg := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/model"))
	arg = strings.TrimSpace(arg)
	if arg == "" {
		current, err := a.agent.CurrentModel(ctx)
		if err != nil {
			a.sendReply(ctx, b, chatID, "Could not read the session model. The local server may be restarting; try again in a moment.")
			return
		}
		a.sendReply(ctx, b, chatID, describeModel(current))
		return
	}
	if arg == "list" {
		catalog, err := a.agent.Catalog(ctx)
		if err != nil {
			a.sendReply(ctx, b, chatID, "Could not read the model catalog. The local server may be restarting; try again in a moment.")
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
	stop := a.keepTyping(ctx, b, chatID)
	err = a.agent.SetModel(ctx, ref)
	stop()
	if err != nil {
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

func (a *app) usage(ctx context.Context, b *bot.Bot, update *models.Update) {
	u, err := a.agent.Usage(ctx)
	if err != nil {
		a.sendReply(ctx, b, update.Message.Chat.ID, "Could not read session usage. The local server may be restarting; try again in a moment.")
		return
	}
	a.sendReply(ctx, b, update.Message.Chat.ID, formatUsage(u))
}

// formatUsage renders one /usage reply from server counters only. Pure
// over the usage value so tests pin it without a server.
func formatUsage(u sessionUsage) string {
	out := "Tokens in: " + commas(u.Input) + ", out: " + commas(u.Output) + "."
	if !u.HasLimit {
		return out + " Session model unknown, so no context window to report."
	}
	pct := u.left() * 100 / u.Limit
	out += " Context left: " + strconv.FormatInt(pct, 10) + "%"
	if u.ModelID != "" {
		out += " on " + u.ModelID
		if v := u.Variant; v != "" && v != "default" {
			out += "#" + v
		}
	}
	return out + "."
}

// commas groups thousands in token counts. Pure so tests pin it.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	digits := strings.TrimPrefix(s, "-")
	if len(digits) <= 3 {
		return s
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	head := len(digits) % 3
	if head == 0 {
		head = 3
	}
	b.WriteString(digits[:head])
	for i := head; i < len(digits); i += 3 {
		b.WriteByte(',')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

func (a *app) ping(ctx context.Context, b *bot.Bot, update *models.Update) {
	a.sendReply(ctx, b, update.Message.Chat.ID, "pong")
}

// startMessage is the short welcome reply for /start.
const startMessage = "Welcome! Send me anything to chat. /new starts a fresh session, /model shows the model, /remember saves a fact."

// memoryTarget splits the optional "<name>:" prefix off a /remember or
// /forget argument. Bare text and unknown prefixes target MEMORY.md.
func memoryTarget(arg string) (file, rest string) {
	if i := strings.IndexByte(arg, ':'); i > 0 {
		if file, ok := targetPrefixes[strings.ToLower(arg[:i])]; ok {
			return file, strings.TrimSpace(arg[i+1:])
		}
	}
	return memoryFile, arg
}

func (a *app) remember(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	arg := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/remember"))
	file, rest := memoryTarget(arg)
	if err := Remember(a.agent.directory, file, rest); err != nil {
		a.sendReply(ctx, b, chatID, "Could not remember that: "+err.Error())
		return
	}
	a.sendReply(ctx, b, chatID, "Remembered in "+file+".")
}

func (a *app) forget(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	arg := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/forget"))
	file, rest := memoryTarget(arg)
	removed, err := Forget(a.agent.directory, file, rest)
	if err != nil {
		a.sendReply(ctx, b, chatID, "Could not forget that: "+err.Error())
		return
	}
	if removed == 0 {
		a.sendReply(ctx, b, chatID, "No lines in "+file+" matched.")
		return
	}
	a.sendReply(ctx, b, chatID, "Forgot the matching lines in "+file+".")
}

func (a *app) start(ctx context.Context, b *bot.Bot, update *models.Update) {
	a.sendReply(ctx, b, update.Message.Chat.ID, startMessage)
}

func (a *app) fresh(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	stop := a.keepTyping(ctx, b, chatID)
	err := a.agent.Reset(ctx)
	stop()
	if err != nil {
		a.sendReply(ctx, b, chatID, "Could not start a fresh session. The local server may be restarting; try again in a moment.")
		return
	}
	a.history.reset()
	a.sendReplySilent(ctx, b, chatID, "Fresh session started.")
}
