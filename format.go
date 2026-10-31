package main

import (
	"errors"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
)

// telegramLimit is Telegram's 4096-character message cap. Converted HTML
// must fit it per chunk since tags and entities add length.
const telegramLimit = 4096

// telegramHTML converts agent Markdown to the Telegram HTML subset
// (b, i, s, tg-spoiler, a, code, pre, blockquote). Everything else is
// escaped or flattened: headings become bold, bullets become lines,
// tables become pre blocks, rules are dropped. Only closed marker pairs
// convert, so a streaming partial with an unclosed marker stays literal.
func telegramHTML(src string) string {
	var out []string
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) {
		if lang, ok := parseFenceOpen(lines[i]); ok {
			var code []string
			i++
			for i < len(lines) && !isFenceClose(lines[i]) {
				code = append(code, escTelegram(lines[i]))
				i++
			}
			if i < len(lines) {
				i++
			}
			body := strings.Join(code, "\n")
			if lang != "" {
				out = append(out, `<pre><code class="language-`+lang+`">`+body+`</code></pre>`)
			} else {
				out = append(out, "<pre>"+body+"</pre>")
			}
			continue
		}
		if isQuoteLine(lines[i]) {
			var qs []string
			for i < len(lines) && isQuoteLine(lines[i]) {
				qs = append(qs, formatLine(stripQuote(lines[i])))
				i++
			}
			out = append(out, "<blockquote>"+strings.Join(qs, "\n")+"</blockquote>")
			continue
		}
		if isTableLine(lines[i]) {
			var rows []string
			for i < len(lines) && isTableLine(lines[i]) {
				rows = append(rows, escTelegram(lines[i]))
				i++
			}
			out = append(out, "<pre>"+strings.Join(rows, "\n")+"</pre>")
			continue
		}
		out = append(out, formatLine(lines[i]))
		i++
	}
	return strings.Join(out, "\n")
}

// formatLine converts one non-fence line: headings to bold, bullets to
// dotted lines, rules to nothing, everything else to inline HTML.
func formatLine(line string) string {
	t := strings.TrimSpace(line)
	if t == "" {
		return ""
	}
	if isHR(t) {
		return ""
	}
	if h, ok := parseHeading(t); ok {
		return "<b>" + renderInline(escTelegram(h)) + "</b>"
	}
	if bullet, ok := parseBullet(t); ok {
		return "• " + renderInline(escTelegram(bullet))
	}
	return renderInline(escTelegram(t))
}

// renderInline runs the inline pipeline over already-escaped text and
// restores protected spans.
func renderInline(escaped string) string {
	r := &restorer{}
	return r.restore(inlineTelegram(escaped, r))
}

// inlineTelegram applies inline markup outside protected spans. Bold runs
// before italic so nested pairs produce nested tags.
func inlineTelegram(s string, r *restorer) string {
	s = extractCodeSpans(s, r)
	s = extractLinks(s, r)
	s = replaceMarked(s, "**", "<b>", "</b>", r)
	s = replaceMarked(s, "__", "<b>", "</b>", r)
	s = replaceMarked(s, "*", "<i>", "</i>", r)
	s = replaceMarked(s, "_", "<i>", "</i>", r)
	s = replaceMarked(s, "~~", "<s>", "</s>", r)
	s = replaceMarked(s, "||", "<tg-spoiler>", "</tg-spoiler>", r)
	return s
}

// escTelegram escapes the three characters Telegram requires outside tags.
func escTelegram(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// restorer hides spans from later markup passes behind NUL placeholders.
type restorer struct {
	parts []string
}

func (r *restorer) stash(html string) string {
	r.parts = append(r.parts, html)
	return "\x00" + strconv.Itoa(len(r.parts)-1) + "\x00"
}

func (r *restorer) restore(s string) string {
	for i, p := range r.parts {
		s = strings.ReplaceAll(s, "\x00"+strconv.Itoa(i)+"\x00", p)
	}
	return s
}

// extractCodeSpans stashes `code` spans so stars inside survive untouched.
func extractCodeSpans(s string, r *restorer) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		o := strings.IndexByte(s[i:], '`')
		if o < 0 {
			b.WriteString(s[i:])
			break
		}
		o += i
		c := strings.IndexByte(s[o+1:], '`')
		if c < 0 {
			b.WriteString(s[i:])
			break
		}
		c += o + 1
		if inner := s[o+1 : c]; inner != "" {
			b.WriteString(s[i:o])
			b.WriteString(r.stash("<code>" + inner + "</code>"))
			i = c + 1
		} else {
			b.WriteString(s[i : o+1])
			i = o + 1
		}
	}
	return b.String()
}

// extractLinks stashes [text](url) spans, formatting the text part. A
// malformed span stays literal.
func extractLinks(s string, r *restorer) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		o := strings.IndexByte(s[i:], '[')
		if o < 0 {
			b.WriteString(s[i:])
			break
		}
		o += i
		m := strings.Index(s[o:], "](")
		if m < 0 {
			b.WriteString(s[i:])
			break
		}
		m += o
		e := strings.IndexByte(s[m+2:], ')')
		if e < 0 {
			b.WriteString(s[i:])
			break
		}
		e += m + 2
		text, url := s[o+1:m], s[m+2:e]
		if text == "" || url == "" || strings.ContainsAny(url, " \t\n") {
			b.WriteString(s[i : o+1])
			i = o + 1
			continue
		}
		b.WriteString(s[i:o])
		url = strings.ReplaceAll(url, `"`, "&quot;")
		b.WriteString(r.stash(`<a href="` + url + `">` + inlineTelegram(text, r) + "</a>"))
		i = e + 1
	}
	return b.String()
}

// isTelegramWord reports a boundary-blocking char: letters, digits, and
// underscore keep snake_case and a*b literal.
func isTelegramWord(c byte) bool {
	return c == '_' ||
		'a' <= c && c <= 'z' ||
		'A' <= c && c <= 'Z' ||
		'0' <= c && c <= '9'
}

// findMarked locates a closed mark...mark pair with non-empty inner. The
// chars around the pair must not be word chars. It returns the match
// bounds and the inner text.
func findMarked(s, mark string) (l, cl int, inner string, ok bool) {
	for base := 0; base+len(mark) <= len(s); {
		rel := strings.Index(s[base:], mark)
		if rel < 0 {
			return 0, 0, "", false
		}
		o := base + rel
		if o > 0 && isTelegramWord(s[o-1]) {
			base = o + 1
			continue
		}
		rest := s[o+len(mark):]
		if !strings.Contains(rest, mark) {
			return 0, 0, "", false
		}
		// Try every closer in order. A closer glued to another mark
		// char belongs to an outer pair, so skip it: without this,
		// **bold and *both*** closes the bold early on the italic's
		// star plus the final star.
		off := 0
		for off <= len(rest) {
			rel2 := strings.Index(rest[off:], mark)
			if rel2 < 0 {
				break
			}
			c := off + rel2
			after := o + len(mark) + c + len(mark)
			boundaryOK := after >= len(s) ||
				(!isTelegramWord(s[after]) && s[after] != mark[0])
			if inner := rest[:c]; inner != "" && boundaryOK {
				return o, after, inner, true
			}
			off = c + 1
		}
		base = o + 1
	}
	return 0, 0, "", false
}

// replaceMarked swaps every closed pair for tags, formatting the inner
// text recursively so nesting stays proper.
func replaceMarked(s, mark, openTag, closeTag string, r *restorer) string {
	var b strings.Builder
	for {
		l, cl, inner, ok := findMarked(s, mark)
		if !ok {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:l])
		b.WriteString(openTag + inlineTelegram(inner, r) + closeTag)
		s = s[cl:]
	}
}

// parseFenceOpen reports a ``` fence and its sanitized language.
func parseFenceOpen(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "```") {
		return "", false
	}
	lang := strings.TrimSpace(strings.TrimPrefix(t, "```"))
	if i := strings.IndexByte(lang, ' '); i >= 0 {
		lang = lang[:i]
	}
	var b strings.Builder
	for i := 0; i < len(lang); i++ {
		c := lang[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '+' || c == '#' || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

func isFenceClose(line string) bool {
	return strings.TrimSpace(line) == "```"
}

func isQuoteLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, ">")
}

func stripQuote(line string) string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, ">")
	return strings.TrimPrefix(t, " ")
}

func isTableLine(line string) bool {
	if strings.Count(line, "|") < 2 {
		return false
	}
	// A ||spoiler|| line is not a table row.
	t := strings.TrimSpace(line)
	return !strings.HasPrefix(t, "||") || !strings.HasSuffix(t, "||")
}

func isHR(line string) bool {
	if len(line) < 3 {
		return false
	}
	for i := 0; i < len(line); i++ {
		if line[i] != '-' && line[i] != '*' && line[i] != '_' && line[i] != ' ' {
			return false
		}
	}
	return strings.TrimSpace(line) != ""
}

// parseHeading strips leading #s from a "# text" heading.
func parseHeading(line string) (string, bool) {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || i >= len(line) || line[i] != ' ' {
		return "", false
	}
	return strings.TrimSpace(line[i+1:]), true
}

// parseBullet strips one "- ", "* ", or "+ " marker.
func parseBullet(line string) (string, bool) {
	if len(line) < 3 {
		return "", false
	}
	if (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' ' {
		return strings.TrimSpace(line[2:]), true
	}
	return "", false
}

// splitMarkdown cuts src so every piece converts to at most limit runes
// of HTML. Cuts land just after a newline, space, or tab, so a piece
// never ends mid-word. Pieces stay valid on their own: an unclosed
// fence closes at the piece end.
func splitMarkdown(src string, limit int) []string {
	var out []string
	rest := []rune(src)
	for len(rest) > 0 {
		n := len(rest)
		if n > maxReplyRunes {
			n = maxReplyRunes
		}
		for {
			if html := telegramHTML(string(rest[:n])); len([]rune(html)) <= limit || n <= 1 {
				break
			} else {
				n = n * limit / len([]rune(html))
				if n < 1 {
					n = 1
				}
			}
		}
		n = splitAtBoundary(rest, n)
		out = append(out, string(rest[:n]))
		rest = rest[n:]
	}
	return out
}

// splitAtBoundary pulls n back to just after the last newline, space, or
// tab inside rest[:n], so a chunk never ends mid-word. It returns n
// unchanged when the cut already lands on whitespace or no whitespace
// fits, e.g. one very long token with no breaks.
func splitAtBoundary(rest []rune, n int) int {
	if n >= len(rest) || n <= 1 {
		return n
	}
	for i := n - 1; i > 0; i-- {
		if rest[i] == '\n' || rest[i] == ' ' || rest[i] == '\t' {
			return i + 1
		}
	}
	return n
}

// isUnparseable reports a Telegram entity error: the HTML was rejected,
// so the caller may resend the plain piece. Other errors must not
// trigger a resend or the message could go out twice.
func isUnparseable(err error) bool {
	return errors.Is(err, bot.ErrorBadRequest) &&
		strings.Contains(err.Error(), "can't parse entities")
}
