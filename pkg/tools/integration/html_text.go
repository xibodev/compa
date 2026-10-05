package integrationtools

import (
	"bytes"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// htmlTextSkip lists elements whose content is never readable text.
var htmlTextSkip = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "math": true, "iframe": true, "object": true, "canvas": true,
}

// htmlTextBlocks lists elements that start and end a line.
var htmlTextBlocks = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "body": true,
	"br": true, "caption": true, "dd": true, "details": true, "dialog": true, "div": true,
	"dl": true, "dt": true, "fieldset": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hr": true, "legend": true, "li": true,
	"main": true, "nav": true, "ol": true, "option": true, "p": true, "section": true,
	"summary": true, "table": true, "tbody": true, "tfoot": true, "thead": true,
	"title": true, "tr": true, "ul": true,
}

// extractHTMLText returns the readable text of an HTML document, one block
// per line. The parser decodes entities and lowercases tag names.
func extractHTMLText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return ""
	}

	w := &htmlTextWriter{}
	// Iterative walk: deeply nested pages must not grow the stack.
	n := doc
	for {
		descend := w.enter(n)
		if descend && n.FirstChild != nil {
			n = n.FirstChild
			continue
		}
		if descend {
			w.leave(n)
		}
		for n != doc && n.NextSibling == nil {
			n = n.Parent
			w.leave(n)
		}
		if n == doc {
			break
		}
		n = n.NextSibling
	}
	w.endLine()
	return strings.TrimRight(w.out.String(), "\n")
}

type htmlTextWriter struct {
	out   strings.Builder
	line  strings.Builder
	space bool
	pre   int
}

func (w *htmlTextWriter) enter(n *html.Node) bool {
	switch n.Type {
	case html.DocumentNode:
		return true
	case html.TextNode:
		w.text(n.Data)
	case html.ElementNode:
		if htmlTextSkip[n.Data] {
			return false
		}
		w.boundary(n.Data, true)
		return true
	}
	return false
}

func (w *htmlTextWriter) leave(n *html.Node) {
	if n.Type == html.ElementNode {
		w.boundary(n.Data, false)
	}
}

func (w *htmlTextWriter) boundary(tag string, opening bool) {
	switch {
	case tag == "pre":
		w.endLine()
		if opening {
			w.pre++
		} else if w.pre > 0 {
			w.pre--
		}
	case tag == "td" || tag == "th":
		w.space = w.line.Len() > 0
	case htmlTextBlocks[tag]:
		w.endLine()
	}
}

func (w *htmlTextWriter) text(s string) {
	if w.pre > 0 {
		for i, part := range strings.Split(s, "\n") {
			if i > 0 {
				w.endLine()
			}
			w.line.WriteString(part)
		}
		return
	}
	for _, r := range s {
		if unicode.IsSpace(r) {
			w.space = w.line.Len() > 0
			continue
		}
		if w.space {
			w.line.WriteByte(' ')
			w.space = false
		}
		w.line.WriteRune(r)
	}
}

func (w *htmlTextWriter) endLine() {
	line := w.line.String()
	if w.pre > 0 {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
	} else {
		line = strings.TrimSpace(line)
	}
	if strings.TrimSpace(line) != "" {
		w.out.WriteString(line)
		w.out.WriteByte('\n')
	}
	w.line.Reset()
	w.space = false
}

// fetchMediaType returns the declared media type, sniffing the body when the
// header is missing, malformed or generic.
func fetchMediaType(contentType string, body []byte) string {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil &&
		mediaType != "application/octet-stream" {
		return mediaType
	}
	mediaType, _, _ := mime.ParseMediaType(http.DetectContentType(body))
	return mediaType
}

func isTextMediaType(mediaType string) bool {
	if strings.HasPrefix(mediaType, "text/") ||
		strings.HasSuffix(mediaType, "+json") ||
		strings.HasSuffix(mediaType, "+xml") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript",
		"application/x-javascript", "application/ecmascript", "application/x-ndjson",
		"application/yaml", "application/x-yaml", "application/toml", "application/x-sh",
		"application/sql", "application/graphql":
		return true
	}
	return false
}

// decodeText converts a body to UTF-8 using the charset named by its BOM,
// the Content-Type header or an HTML <meta> tag. An undeclared body that is
// already valid UTF-8 is kept as is, since the sniffed default only looks at
// the first kilobyte.
func decodeText(body []byte, contentType string) string {
	enc, name, certain := charset.DetermineEncoding(body, contentType)
	if enc != nil && name != "utf-8" && (certain || !utf8.Valid(body)) {
		if decoded, err := enc.NewDecoder().Bytes(body); err == nil {
			body = decoded
		}
	}
	body = bytes.TrimPrefix(body, []byte("\uFEFF"))
	return strings.ToValidUTF8(string(body), "\uFFFD")
}

func looksLikeHTML(body string) bool {
	head := strings.TrimLeft(body, " \t\r\n")
	if len(head) > 16 {
		head = head[:16]
	}
	head = strings.ToLower(head)
	return strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html")
}

// truncateRunes cuts s to at most n characters without splitting one, and
// reports the original character count.
func truncateRunes(s string, n int) (string, int, bool) {
	total := utf8.RuneCountInString(s)
	if total <= n {
		return s, total, false
	}
	end := 0
	for range n {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return s[:end], total, true
}
