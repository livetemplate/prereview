package gitdiff

import (
	"regexp"
	"strings"
)

// The HTML preview's iframe is as tall as its page (lvt-fx:preview-bridge sets
// its height from the page's), so inside it a vh is a percentage of the page
// itself. A page whose vh heights add up to 100% or more (one min-height:100vh
// hero is enough) then grows by at least as much as the frame does, on every
// resize, until the browser's height limit. rewriteViewportUnits pins those
// units to a fixed basis instead: N vh becomes N × --prereview-vh, which the
// preview bridge sets from the screen height before the page lays out. The px
// fallback keeps the height finite if the beacon never runs.
//
// The block-axis units (vb and its s/l/d forms) equal vh in horizontal writing,
// and in a frame taller than it is wide vmax is vh too, so they are rewritten
// the same way. vw and vmin don't follow the frame's height and stay as they are.

// viewportHeightVar is the custom property the preview bridge sets to 1% of
// the screen height.
const viewportHeightVar = "--prereview-vh"

// viewportUnitRe matches a CSS number with a height-coupled viewport unit. The
// character before it must not be part of a name, so a class like
// ".hero-10vh" or a file name in url(a-10vh.png) is left alone.
var viewportUnitRe = regexp.MustCompile(`(?i)(^|[^\w.#-])(-?(?:\d+(?:\.\d+)?|\.\d+))([sld]?(?:vh|vb)|[sld]?vmax)\b`)

// rewriteViewportUnits rewrites the height-coupled viewport units in CSS text
// (a <style> body or a style attribute) to a fixed basis. It never adds or
// removes a newline, so the preview's source line ranges stay put.
func rewriteViewportUnits(css string) string {
	if !strings.Contains(strings.ToLower(css), "v") {
		return css
	}
	return viewportUnitRe.ReplaceAllStringFunc(css, func(m string) string {
		sub := viewportUnitRe.FindStringSubmatch(m)
		prefix, num, unit := sub[1], sub[2], strings.ToLower(sub[3])
		pinned := "calc(" + num + " * var(" + viewportHeightVar + ", 9px))"
		if strings.HasSuffix(unit, "vmax") {
			return prefix + "max(" + num + "vw, " + pinned + ")"
		}
		return prefix + pinned
	})
}

// rewriteStyleAttrs applies rewriteViewportUnits to a tag's style attribute.
func rewriteStyleAttrs(attrs []rawAttr) []rawAttr {
	for i, a := range attrs {
		if strings.EqualFold(a.key, "style") {
			attrs[i].val = rewriteViewportUnits(a.val)
		}
	}
	return attrs
}
