package tools

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	htmlListItemPrefix = "- "
	htmlLinkOpen       = " ("
	htmlLinkClose      = ")"
)

//nolint:gochecknoglobals // Read-only lookup tables and compiled patterns.
var (
	// htmlSkippedElements hold no text a reader sees.
	htmlSkippedElements = map[atom.Atom]bool{
		atom.Script: true, atom.Style: true, atom.Noscript: true,
		atom.Template: true, atom.Svg: true, atom.Iframe: true,
		atom.Head: true, atom.Object: true, atom.Canvas: true,
	}

	// htmlBlockElements start and end on their own line.
	htmlBlockElements = map[atom.Atom]bool{
		atom.Address: true, atom.Article: true, atom.Aside: true,
		atom.Blockquote: true, atom.Br: true, atom.Dd: true, atom.Details: true,
		atom.Div: true, atom.Dl: true, atom.Dt: true, atom.Figcaption: true,
		atom.Figure: true, atom.Footer: true, atom.Form: true, atom.H1: true,
		atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true,
		atom.H6: true, atom.Header: true, atom.Hr: true, atom.Li: true,
		atom.Main: true, atom.Nav: true, atom.Ol: true, atom.P: true,
		atom.Pre: true, atom.Section: true, atom.Summary: true,
		atom.Table: true, atom.Tr: true, atom.Ul: true,
	}

	htmlSpaceRun     = regexp.MustCompile(`[ \t\r\n\f]+`)
	htmlBlankLineRun = regexp.MustCompile(`\n{3,}`)
)

type htmlPage struct {
	title string
	text  string
}

type htmlTextWriter struct {
	builder strings.Builder
	base    *url.URL
	inPre   int
}

// htmlText reduces an HTML document to the text a reader sees: no scripts,
// styles, or markup, one line per block, list items marked, and each link's
// absolute target after its text.
func htmlText(document string, base *url.URL) htmlPage {
	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return htmlPage{text: document}
	}

	writer := &htmlTextWriter{base: base}
	writer.walk(root)

	return htmlPage{
		title: htmlTitle(root),
		text:  tidyHTMLText(writer.builder.String()),
	}
}

func (w *htmlTextWriter) walk(node *html.Node) {
	switch node.Type {
	case html.TextNode:
		w.text(node.Data)

		return
	case html.ElementNode:
		w.element(node)

		return
	case html.ErrorNode, html.DocumentNode, html.CommentNode,
		html.DoctypeNode, html.RawNode:
	}

	w.children(node)
}

func (w *htmlTextWriter) children(node *html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		w.walk(child)
	}
}

func (w *htmlTextWriter) element(node *html.Node) {
	if htmlSkippedElements[node.DataAtom] {
		return
	}

	isBlock := htmlBlockElements[node.DataAtom]
	if isBlock {
		w.builder.WriteByte('\n')
	}

	if node.DataAtom == atom.Li {
		w.builder.WriteString(htmlListItemPrefix)
	}

	if node.DataAtom == atom.Pre {
		w.inPre++
		defer func() { w.inPre-- }()
	}

	w.children(node)

	if node.DataAtom == atom.A {
		w.link(node)
	}

	if isBlock {
		w.builder.WriteByte('\n')
	}
}

func (w *htmlTextWriter) text(data string) {
	if w.inPre > 0 {
		w.builder.WriteString(data)

		return
	}

	collapsed := htmlSpaceRun.ReplaceAllString(data, " ")
	if w.atLineStart() {
		collapsed = strings.TrimLeft(collapsed, " ")
	}

	w.builder.WriteString(collapsed)
}

func (w *htmlTextWriter) atLineStart() bool {
	written := w.builder.String()

	return written == "" || strings.HasSuffix(written, "\n") ||
		strings.HasSuffix(written, htmlListItemPrefix)
}

// link writes an http or https target after the link text. Fragments and
// script links point nowhere a fetch can follow, so they are left out.
func (w *htmlTextWriter) link(node *html.Node) {
	href := htmlAttribute(node, "href")
	if href == "" || strings.HasPrefix(href, "#") {
		return
	}

	target, err := url.Parse(href)
	if err != nil {
		return
	}

	if w.base != nil {
		target = w.base.ResolveReference(target)
	}

	if !isWebFetchScheme(target.Scheme) {
		return
	}

	w.builder.WriteString(htmlLinkOpen + target.String() + htmlLinkClose)
}

func htmlAttribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Namespace == "" && attribute.Key == name {
			return strings.TrimSpace(attribute.Val)
		}
	}

	return ""
}

func htmlTitle(node *html.Node) string {
	if node.Type == html.ElementNode && node.DataAtom == atom.Title {
		var title strings.Builder

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.TextNode {
				title.WriteString(child.Data)
			}
		}

		return strings.TrimSpace(htmlSpaceRun.ReplaceAllString(
			title.String(), " "))
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if title := htmlTitle(child); title != "" {
			return title
		}
	}

	return ""
}

// tidyHTMLText drops trailing space from every line and keeps at most one
// blank line between blocks. Leading space stays, because it is either
// preformatted indentation or already gone.
func tidyHTMLText(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t\r")
	}

	text = htmlBlankLineRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")

	return strings.TrimSpace(text)
}
