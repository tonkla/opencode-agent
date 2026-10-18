package main

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// chatHistory appends the Telegram conversation to one Markdown file per
// agent session: history/YYYYMMDD/HHMM.md under the workspace, named for
// the session start. Entries read "**User**: ..." and "**Agent**: ..."
// separated by a blank line. A new file starts at boot and on every /new.
type chatHistory struct {
	mu      sync.Mutex
	baseDir string
	clock   func() time.Time
	started time.Time
}

func newChatHistory(dir string) *chatHistory {
	h := &chatHistory{baseDir: dir, clock: time.Now}
	h.started = h.clock()
	return h
}

// reset starts a new history file for the fresh session. Call it only after
// the session actually restarted so a failed /new keeps appending in place.
func (h *chatHistory) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = h.clock()
}

// pathFor resolves the file for a session start in local time.
func pathFor(base string, start time.Time) string {
	return filepath.Join(base, "history", start.Format("20060102"), start.Format("1504")+".md")
}

func (h *chatHistory) append(role, text string) {
	if text == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// The stamp records when the line was written, so the gap between a
	// user line and the next agent line shows how long the answer took.
	stamp := h.clock().Format("15:04:05")
	path := pathFor(h.baseDir, h.started)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("chat history: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("chat history: %v", err)
		return
	}
	// History must never break chat, so a failed write only reaches the
	// process log, not the conversation.
	if _, err := f.WriteString("**" + role + "** _" + stamp + "_: " + text + "\n\n"); err != nil {
		log.Printf("chat history: %v", err)
	}
	if err := f.Close(); err != nil {
		log.Printf("chat history: %v", err)
	}
}

// appendUser records one allowed incoming message.
func (h *chatHistory) appendUser(text string) { h.append("User", text) }

// appendAgent records one outgoing reply. Callers pass the whole reply;
// sendReply chunks it for Telegram afterwards.
func (h *chatHistory) appendAgent(text string) { h.append("Agent", text) }
