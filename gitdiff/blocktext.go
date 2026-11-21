package gitdiff

import (
	"html/template"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// BlockText is a rendered Markdown block's visible text, as a reader of the
// rendered page sees it: tags dropped, entities decoded, a space between
// table cells and list items. A quote copied from a rendered copy of the doc
// (where `**b**` shows as `b`) matches this, not the source.
func BlockText(h template.HTML) string {
	nodes, err := html.ParseFragment(strings.NewReader(string(h)), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case n.Type == html.ElementNode && (n.DataAtom == atom.Br || n.DataAtom == atom.Td || n.DataAtom == atom.Th || n.DataAtom == atom.Li):
			b.WriteByte(' ')
		}
		for k := n.FirstChild; k != nil; k = k.NextSibling {
			walk(k)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return b.String()
}
