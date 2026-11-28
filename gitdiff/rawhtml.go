package gitdiff

import (
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Raw HTML in Markdown renders sanitized, as GitHub's preview does: a page
// built from <section>/<div>/<header> blocks (a landing page, a README hero)
// shows its text, headings, links, lists, tables and local images instead of
// an empty block per tag.
//
// SECURITY: the output lands in template.HTML and bypasses the autoescaper, so
// this ALLOWLISTS on a parsed tree; it never filters a string:
//   - each HTML block is parsed as a fragment, so its tags come out balanced
//     and a stray </div> can't close the preview's own markup;
//   - an element outside the allowlist is unwrapped (its text stays), and the
//     ones that run code, load content or take input are dropped with
//     everything inside them (dropWhole);
//   - attributes are allowlisted per element: no on*, no style, no class or
//     id (the site's CSS isn't loaded, and an id could clash with the page's);
//   - an href must be relative, a #fragment, http(s) or mailto; an <img> must
//     pass the same local-only rules as a lone raw image (buildSafeImg);
//   - html.Render re-escapes every text node and attribute value.
// The site's own styling doesn't apply, so the result looks plain, as GitHub's
// does.

// keepTags are the elements a raw HTML block keeps, with the attributes each
// may carry besides the shared keepAttrs.
var keepTags = map[atom.Atom][]string{
	atom.Div: {"align"}, atom.Section: nil, atom.Article: nil, atom.Aside: nil,
	atom.Header: nil, atom.Footer: nil, atom.Main: nil, atom.Nav: nil,
	atom.P: {"align"}, atom.Span: nil, atom.Br: nil, atom.Hr: nil,
	atom.H1: {"align"}, atom.H2: {"align"}, atom.H3: {"align"},
	atom.H4: {"align"}, atom.H5: {"align"}, atom.H6: {"align"},
	atom.Ul: nil, atom.Ol: {"start", "reversed"}, atom.Li: nil,
	atom.Dl: nil, atom.Dt: nil, atom.Dd: nil, atom.Blockquote: nil,
	atom.Pre: nil, atom.Code: nil, atom.Kbd: nil, atom.Samp: nil, atom.Var: nil,
	atom.Em: nil, atom.Strong: nil, atom.B: nil, atom.I: nil, atom.U: nil,
	atom.S: nil, atom.Del: nil, atom.Ins: nil, atom.Mark: nil, atom.Small: nil,
	atom.Sub: nil, atom.Sup: nil, atom.Abbr: nil, atom.Cite: nil, atom.Q: nil,
	atom.Time: {"datetime"},
	atom.Table: nil, atom.Caption: nil, atom.Thead: nil, atom.Tbody: nil,
	atom.Tfoot: nil, atom.Tr: nil, atom.Th: {"colspan", "rowspan", "align"},
	atom.Td: {"colspan", "rowspan", "align"},
	atom.Details: {"open"}, atom.Summary: nil, atom.Figure: nil, atom.Figcaption: nil,
	atom.A: {"href"}, atom.Img: nil, // img attributes: buildSafeImg
}

// keepAttrs are allowed on every kept element.
var keepAttrs = []string{"title", "lang", "dir"}

// dropWhole are removed with their contents: they run code, load other
// content, take input, or (svg, math) carry their own script and link surface.
var dropWhole = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true,
	atom.Embed: true, atom.Form: true, atom.Input: true, atom.Button: true,
	atom.Select: true, atom.Textarea: true, atom.Template: true,
	atom.Noscript: true, atom.Link: true, atom.Meta: true, atom.Base: true,
	atom.Svg: true, atom.Math: true, atom.Canvas: true, atom.Video: true,
	atom.Audio: true, atom.Source: true, atom.Track: true, atom.Frame: true,
	atom.Frameset: true, atom.Applet: true, atom.Title: true, atom.Head: true,
}

// sanitizeHTMLBlock renders a raw HTML block through the allowlist. It
// returns "" when nothing in the block survives (a comment, a lone <script>).
func sanitizeHTMLBlock(raw string) string {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(raw), body)
	if err != nil {
		return ""
	}
	var kept []*html.Node
	for _, n := range nodes {
		kept = append(kept, sanitizeNode(n)...)
	}
	// A block that only opens or closes a wrapper (`<main>`, `</section>`)
	// balances to an empty element; it would draw an empty block.
	if !slices.ContainsFunc(kept, showsSomething) {
		return ""
	}
	var b strings.Builder
	for _, n := range kept {
		if err := html.Render(&b, n); err != nil {
			return ""
		}
	}
	return b.String()
}

// showsSomething reports whether n renders anything visible: text, an image
// or a rule.
func showsSomething(n *html.Node) bool {
	if n.Type == html.TextNode {
		return strings.TrimSpace(n.Data) != ""
	}
	if n.DataAtom == atom.Img || n.DataAtom == atom.Hr {
		return true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if showsSomething(c) {
			return true
		}
	}
	return false
}

// sanitizeNode returns what n becomes: itself, cleaned; its sanitized
// children, when n itself isn't allowed; or nothing.
func sanitizeNode(n *html.Node) []*html.Node {
	switch n.Type {
	case html.TextNode:
		return []*html.Node{{Type: html.TextNode, Data: n.Data}}
	case html.ElementNode:
	default: // comments, doctypes
		return nil
	}
	if dropWhole[n.DataAtom] {
		return nil
	}
	var kids []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		kids = append(kids, sanitizeNode(c)...)
	}
	extra, ok := keepTags[n.DataAtom]
	if !ok || n.Namespace != "" {
		return kids // unknown element: keep its content
	}
	out := &html.Node{Type: html.ElementNode, Data: n.Data, DataAtom: n.DataAtom}
	if n.DataAtom == atom.Img {
		var attrs []rawAttr
		for _, a := range n.Attr {
			attrs = append(attrs, rawAttr{a.Key, a.Val})
		}
		if _, ok := buildSafeImg(attrs); !ok {
			return nil
		}
		for _, a := range n.Attr {
			if k := strings.ToLower(a.Key); k == "src" || allowedImgAttr[k] {
				out.Attr = append(out.Attr, html.Attribute{Key: k, Val: a.Val})
			}
		}
		return []*html.Node{out}
	}
	for _, a := range n.Attr {
		k := strings.ToLower(a.Key)
		if a.Namespace != "" || !(slices.Contains(keepAttrs, k) || slices.Contains(extra, k)) {
			continue
		}
		if k == "href" && !safeHref(a.Val) {
			continue
		}
		if k == "align" && !allowedAlign[strings.ToLower(strings.TrimSpace(a.Val))] {
			continue
		}
		out.Attr = append(out.Attr, html.Attribute{Key: k, Val: a.Val})
	}
	for _, k := range kids {
		out.AppendChild(k)
	}
	return []*html.Node{out}
}

// sanitizeInlineTag renders one inline raw HTML tag (goldmark hands inline
// HTML over a tag at a time, so there is no fragment to balance): an allowed
// phrasing tag keeps its allowed attributes, a closing tag passes, and
// anything else is dropped.
func sanitizeInlineTag(raw string) string {
	z := html.NewTokenizer(strings.NewReader(raw))
	switch tt := z.Next(); tt {
	case html.StartTagToken, html.SelfClosingTagToken:
		name, _ := z.TagName()
		a := atom.Lookup(name)
		if !inlineTags[a] {
			return ""
		}
		n := &html.Node{Type: html.ElementNode, Data: a.String(), DataAtom: a}
		var attrs []html.Attribute
		for {
			k, v, more := z.TagAttr()
			if len(k) > 0 {
				attrs = append(attrs, html.Attribute{Key: string(k), Val: string(v)})
			}
			if !more {
				break
			}
		}
		n.Attr = attrs
		kept := sanitizeNode(n)
		if len(kept) != 1 {
			return ""
		}
		var b strings.Builder
		_ = html.Render(&b, kept[0])
		s := b.String()
		if a != atom.Br {
			s = strings.TrimSuffix(s, "</"+a.String()+">") // html.Render closes it; the source's own close tag follows
		}
		return s
	case html.EndTagToken:
		name, _ := z.TagName()
		if a := atom.Lookup(name); inlineTags[a] && a != atom.Br && a != atom.Img {
			return "</" + a.String() + ">"
		}
	}
	return ""
}

// inlineTags are the phrasing elements inline raw HTML may use.
var inlineTags = map[atom.Atom]bool{
	atom.Kbd: true, atom.Code: true, atom.Samp: true, atom.Var: true,
	atom.Em: true, atom.Strong: true, atom.B: true, atom.I: true, atom.U: true,
	atom.S: true, atom.Del: true, atom.Ins: true, atom.Mark: true,
	atom.Small: true, atom.Sub: true, atom.Sup: true, atom.Abbr: true,
	atom.Cite: true, atom.Q: true, atom.Span: true, atom.Br: true,
	atom.A: true, atom.Time: true,
}

// safeHref allows a relative URL, a #fragment, http(s) and mailto.
func safeHref(v string) bool {
	v = strings.TrimSpace(v)
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https", "mailto":
		return !strings.ContainsAny(v, "\x00\t\n\r") // a scheme split by whitespace is not ""
	}
	return false
}
