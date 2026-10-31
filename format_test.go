package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/go-telegram/bot"
)

func TestTelegramHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bold stars", "**bold**", "<b>bold</b>"},
		{"bold under", "__bold__", "<b>bold</b>"},
		{"italic star", "*italic*", "<i>italic</i>"},
		{"italic under", "_italic_", "<i>italic</i>"},
		{"nested", "**bold and *both***", "<b>bold and <i>both</i></b>"},
		{"strike", "~~gone~~", "<s>gone</s>"},
		{"spoiler", "||later||", "<tg-spoiler>later</tg-spoiler>"},
		{"code span", "`a*b`", "<code>a*b</code>"},
		{"code protects stars", "`**x**`", "<code>**x**</code>"},
		{"snake untouched", "foo_bar_baz", "foo_bar_baz"},
		{"math untouched", "2*2=4", "2*2=4"},
		{"pointer untouched", "a*b", "a*b"},
		{"link", "[docs](http://example.com/x)", `<a href="http://example.com/x">docs</a>`},
		{"link text formats", "[**docs**](http://example.com/)", `<a href="http://example.com/"><b>docs</b></a>`},
		{"heading", "## Title", "<b>Title</b>"},
		{"hashtag untouched", "#high", "#high"},
		{"bullet dash", "- item", "• item"},
		{"bullet star", "* item", "• item"},
		{"numbered kept", "1. item", "1. item"},
		{"quote", "> cited", "<blockquote>cited</blockquote>"},
		{"rule dropped", "---", ""},
		{"escape", "a<b>&c", "a&lt;b&gt;&amp;c"},
		{"unclosed literal", "**half", "**half"},
		{"table pre", "| a | b |\n|---|---|", "<pre>| a | b |\n|---|---|</pre>"},
		{"fence lang", "```go\nfmt.Println()\n```", "<pre><code class=\"language-go\">fmt.Println()</code></pre>"},
		{"fence plain", "```\nx\n```", "<pre>x</pre>"},
		{"unclosed fence", "```go\nx", "<pre><code class=\"language-go\">x</code></pre>"},
		{"model line", "deepseek-v4.1-flash-free [low, high]", "deepseek-v4.1-flash-free [low, high]"},
		{"usage line", "Tokens in: 1,234, out: 5,678.", "Tokens in: 1,234, out: 5,678."},
	}
	for _, c := range cases {
		if got := telegramHTML(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestTelegramHTMLHistoryReply converts the shape of a real agent reply:
// headings, bold, lists, and fenced code must all render as HTML.
func TestTelegramHTMLHistoryReply(t *testing.T) {
	in := "## Effect v4\n\nEffect is a **functional effect system**.\n\n- **typed errors** in the signature\n\n```ts\nEffect<A, E, R>\n```\n\nSee [docs](http://example.com/)."
	got := telegramHTML(in)
	for _, want := range []string{
		"<b>Effect v4</b>",
		"<b>functional effect system</b>",
		"• <b>typed errors</b>",
		`<pre><code class="language-ts">Effect&lt;A, E, R&gt;</code></pre>`,
		`<a href="http://example.com/">docs</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "**") {
		t.Errorf("raw markers leaked:\n%s", got)
	}
}

// TestSplitMarkdownKeepsWordsWhole reproduces a Telegram render where a
// long reply was cut mid-word ("aftertho" / "ught"). No piece boundary
// may land inside a word when the text has whitespace to break at.
func TestSplitMarkdownKeepsWordsWhole(t *testing.T) {
	src := "3. Reading state is first-class, not an afterthought at all here"
	split := 0
	for limit := 15; limit <= 70; limit++ {
		pieces := splitMarkdown(src, limit)
		if len(pieces) < 2 {
			continue
		}
		split++
		if joined := strings.Join(pieces, ""); joined != src {
			t.Errorf("limit %d: split lost content: got %q want %q", limit, joined, src)
		}
		for _, p := range pieces {
			if html := telegramHTML(p); len([]rune(html)) > limit {
				t.Errorf("limit %d: piece exceeds limit: %q", limit, p)
			}
		}
		for i := 0; i+1 < len(pieces); i++ {
			last := []rune(pieces[i])
			next := []rune(pieces[i+1])
			if len(last) == 0 || len(next) == 0 {
				continue
			}
			if isWordRune(last[len(last)-1]) && isWordRune(next[0]) {
				t.Errorf("limit %d: piece %d breaks a word: %q | %q", limit, i, pieces[i], pieces[i+1])
			}
		}
	}
	if split == 0 {
		t.Fatal("expected splits at some limit, got none")
	}
}

// isWordRune reports a rune that continues a word across a split.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func TestIsUnparseable(t *testing.T) {
	parseErr := fmt.Errorf("%w, %s", bot.ErrorBadRequest, "Bad Request: can't parse entities: ...")
	if !isUnparseable(parseErr) {
		t.Error("entity error must trigger fallback")
	}
	otherBad := fmt.Errorf("%w, %s", bot.ErrorBadRequest, "Bad Request: message is too long")
	if isUnparseable(otherBad) {
		t.Error("length error must not trigger fallback")
	}
	if isUnparseable(nil) {
		t.Error("nil must not trigger fallback")
	}
	if isUnparseable(fmt.Errorf("error do request: dial tcp")) {
		t.Error("network error must not trigger fallback and double-send")
	}
}

// TestSendChunksFallback proves a rejected HTML send is retried once as
// plain text, while an accepted send goes out once as HTML.
func TestSendChunksFallback(t *testing.T) {
	var calls atomic.Int64
	var modes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			_ = r.ParseMultipartForm(1 << 20)
			n := calls.Add(1)
			modes = append(modes, r.FormValue("parse_mode"))
			if n == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: can't parse entities","error_code":400}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"t"}}`))
	}))
	defer srv.Close()
	b, err := bot.New("test-token", bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	(&app{}).sendChunks(context.Background(), b, 9, "**hi**")
	if calls.Load() != 2 {
		t.Fatalf("want HTML send plus plain retry, got %d calls", calls.Load())
	}
	if len(modes) != 2 || modes[0] != "HTML" || modes[1] != "" {
		t.Errorf("modes = %q, want [HTML plain]", modes)
	}
}
