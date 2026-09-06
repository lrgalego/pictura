package web

import (
	"html"
	"regexp"
	"strings"

	"github.com/lrgalego/htmx-ds/sanitize"
	xhtml "golang.org/x/net/html"
)

// scriptFromHTML takes what the editor posted and returns the sanitized
// HTML to keep for re-editing plus the plain text the models read, with
// block elements and line breaks turned into newlines.
func scriptFromHTML(posted string) (clean, text string) {
	clean = sanitize.HTML(posted)
	doc, err := xhtml.Parse(strings.NewReader(clean))
	if err != nil {
		return clean, strings.TrimSpace(sanitize.Text(clean))
	}
	var b strings.Builder
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		switch n.Type {
		case xhtml.TextNode:
			b.WriteString(n.Data)
			return
		case xhtml.ElementNode:
			switch n.Data {
			case "br":
				b.WriteString("\n")
				return
			case "p", "div", "li", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre", "tr":
				b.WriteString("\n")
				defer b.WriteString("\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return clean, tidyLines(b.String())
}

var manyBlank = regexp.MustCompile(`\n{3,}`)

// tidyLines trims each line, keeps paragraph breaks, drops runs of them.
func tidyLines(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	out := strings.Join(lines, "\n")
	out = manyBlank.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// htmlFromText renders plain text as editor HTML: paragraphs on blank
// lines, line breaks within them. For scripts saved before the editor.
func htmlFromText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var b strings.Builder
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		b.WriteString("<p>")
		for i, line := range strings.Split(para, "\n") {
			if i > 0 {
				b.WriteString("<br>")
			}
			b.WriteString(html.EscapeString(line))
		}
		b.WriteString("</p>")
	}
	return b.String()
}

// scriptInput reads the script from a form: the editor's HTML when present,
// a plain textarea/API value otherwise.
func scriptInput(htmlValue, textValue string) (clean, text string) {
	if strings.TrimSpace(htmlValue) != "" {
		return scriptFromHTML(htmlValue)
	}
	text = tidyLines(strings.ReplaceAll(strings.ReplaceAll(textValue, "\r\n", "\n"), "\r", "\n"))
	return htmlFromText(text), text
}
