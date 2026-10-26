package main

import (
	"context"
	"log"
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
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, a.requireUser(a.help))
}

// menuCommands lists every slash command shown in the Telegram menu, one
// entry per registered handler. Pure so tests pin it.
func menuCommands() []models.BotCommand {
	return []models.BotCommand{
		{Command: "start", Description: "Start a fresh session (alias of /new)"},
		{Command: "ping", Description: "Check the bot is alive"},
		{Command: "new", Description: "Start a fresh session"},
		{Command: "model", Description: "Show or switch the session model"},
		{Command: "usage", Description: "Show token usage"},
		{Command: "remember", Description: "Save a fact"},
		{Command: "forget", Description: "Forget matching lines"},
		{Command: "help", Description: "List all commands"},
	}
}

// helpText renders the /help reply from the menu list, one command per
// line. Built from menuCommands so the reply cannot drift from the menu.
func helpText() string {
	var b strings.Builder
	for _, c := range menuCommands() {
		b.WriteString("/")
		b.WriteString(c.Command)
		b.WriteString(" - ")
		b.WriteString(c.Description)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// publishCommands exposes menuCommands in the Telegram menu. A failed
// publish only reaches the process log: the menu is cosmetic and must
// never keep the bot from starting.
func (a *app) publishCommands(ctx context.Context, b *bot.Bot) {
	if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: menuCommands()}); err != nil {
		log.Printf("telegram menu: %v", err)
	}
}

func (a *app) model(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID
	sess, err := a.sessionFor(userID)
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not open your workspace; try again in a moment.")
		return
	}
	arg := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/model"))
	arg = strings.TrimSpace(arg)
	if arg == "" {
		current, err := sess.agent.CurrentModel(ctx)
		if err != nil {
			a.sendReply(ctx, b, chatID, userID, "Could not read the session model. The local server may be restarting; try again in a moment.")
			return
		}
		a.sendReply(ctx, b, chatID, userID, describeModel(current))
		return
	}
	if arg == "list" {
		catalog, err := sess.agent.Catalog(ctx)
		if err != nil {
			a.sendReply(ctx, b, chatID, userID, "Could not read the model catalog. The local server may be restarting; try again in a moment.")
			return
		}
		a.sendReply(ctx, b, chatID, userID, formatCatalog(catalog))
		return
	}
	ref, err := ParseModelRef(arg)
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, err.Error()+suggestSuffix(ctx, sess.agent, arg))
		return
	}
	stop := a.keepTyping(ctx, b, chatID)
	err = sess.agent.SetModel(ctx, ref)
	stop()
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not switch model: "+err.Error())
		return
	}
	a.sendReply(ctx, b, chatID, userID, "Model set to "+ref.String()+" for this session.")
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
	userID := update.Message.From.ID
	sess, err := a.sessionFor(userID)
	if err != nil {
		a.sendReply(ctx, b, update.Message.Chat.ID, userID, "Could not open your workspace; try again in a moment.")
		return
	}
	u, err := sess.agent.Usage(ctx)
	if err != nil {
		a.sendReply(ctx, b, update.Message.Chat.ID, userID, "Could not read session usage. The local server may be restarting; try again in a moment.")
		return
	}
	a.sendReply(ctx, b, update.Message.Chat.ID, userID, formatUsage(u))
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
	a.sendReply(ctx, b, update.Message.Chat.ID, update.Message.From.ID, "pong")
}

// unknownCommandReply answers a slash command the bot does not know.
// It never reaches the agent.
const unknownCommandReply = "Unknown command"

// commandName extracts the slash command from text, dropping arguments
// and an optional @bot suffix. It reports false when text holds no
// command, so bare "/" and "/ ..." stay plain text for the agent.
func commandName(text string) (string, bool) {
	rest, ok := strings.CutPrefix(text, "/")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(rest, " ")
	name, _, _ = strings.Cut(name, "@")
	if name == "" {
		return "", false
	}
	return name, true
}

// isKnownCommand reports whether text invokes a handled slash command.
func isKnownCommand(text string) bool {
	name, ok := commandName(text)
	if !ok {
		return false
	}
	switch name {
	case "start", "ping", "new", "model", "usage", "remember", "forget", "help":
		return true
	}
	return false
}

// isUnknownCommand reports a slash command the bot does not handle.
func isUnknownCommand(text string) bool {
	_, ok := commandName(text)
	return ok && !isKnownCommand(text)
}

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
	userID := update.Message.From.ID
	sess, err := a.sessionFor(userID)
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not open your workspace; try again in a moment.")
		return
	}
	arg := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/remember"))
	file, rest := memoryTarget(arg)
	if err := Remember(sess.agent.directory, file, rest); err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not remember that: "+err.Error())
		return
	}
	a.sendReply(ctx, b, chatID, userID, "Remembered in "+file+".")
}

func (a *app) forget(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID
	sess, err := a.sessionFor(userID)
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not open your workspace; try again in a moment.")
		return
	}
	arg := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/forget"))
	file, rest := memoryTarget(arg)
	removed, err := Forget(sess.agent.directory, file, rest)
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not forget that: "+err.Error())
		return
	}
	if removed == 0 {
		a.sendReply(ctx, b, chatID, userID, "No lines in "+file+" matched.")
		return
	}
	a.sendReply(ctx, b, chatID, userID, "Forgot the matching lines in "+file+".")
}

// start is an alias of /new: it delegates to fresh so both commands
// restart the session the same way.
func (a *app) start(ctx context.Context, b *bot.Bot, update *models.Update) {
	a.fresh(ctx, b, update)
}

func (a *app) help(ctx context.Context, b *bot.Bot, update *models.Update) {
	a.sendReplySilent(ctx, b, update.Message.Chat.ID, helpText())
}

func (a *app) fresh(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID
	sess, err := a.sessionFor(userID)
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not open your workspace; try again in a moment.")
		return
	}
	stop := a.keepTyping(ctx, b, chatID)
	err = sess.agent.Reset(ctx)
	stop()
	if err != nil {
		a.sendReply(ctx, b, chatID, userID, "Could not start a fresh session. The local server may be restarting; try again in a moment.")
		return
	}
	sess.history.reset()
	a.sendReplySilent(ctx, b, chatID, "Fresh session started.")
}
