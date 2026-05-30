# Image-area annotations fixture

This `diagram.png` is the binary half of the issue #16 phase-2 fixture
— a 256×256 PNG with four distinctly-coloured quadrants so a developer
or a chromedp test can drag a rectangle over a known region and visually
verify the overlay landed in the right place.

## Why this fixture exists

Issue #16's phase 2 closes the last gap on "comment on whatever":

- Phase 1 (`v0.3.4`) added a "Comment on file" affordance that works
  on every file type but anchors to the whole file.
- Phase 2 (this PR) adds **per-region** image annotations: you drag a
  rectangle over a part of the image, type a comment, save, and the
  comment persists with a CSV `kind=area` row whose 12th column holds
  the rectangle as `{"x":...,"y":...,"w":...,"h":...}` — each value a
  0..1 fraction of the image's natural dimensions.

The drag selection is handled by a new `lvt-fx:area-select` directive
in the livetemplate client (shipped first in the matching client PR).
The client paints the in-progress overlay locally — no server
round-trip per pointermove — and dispatches a single action with the
final coords on pointerup. From the prereview side, the directive is
just another `lvt-fx:*` attribute on the `<img>`.

## How to drive prereview against this fixture by hand

```sh
mkdir /tmp/ac && cd /tmp/ac && git init -q -b main \
  && git config user.email t@t && git config user.name t \
  && git config commit.gpgsign false
cp <PREREVIEW>/testdata/areacomments/* .
git add -A && git commit -q -m seed
# Make the diff non-empty:
git mv diagram.png renamed.png
prereview --repo $(pwd)
```

Then in the browser:

1. Click `renamed.png` in the sidebar. The image renders inline.
2. Click and drag a rectangle over the **blue (top-left)** quadrant.
   On release: an overlay rectangle stays visible, and a composer
   appears in the file-comments section above the image.
3. Type "this colour should be brighter" and Save. The overlay
   becomes permanent; the list entry below carries Resolve / Edit /
   Delete.
4. Drag another rectangle over the **red (top-right)** quadrant and
   save a second comment. Both overlays are visible at once.
5. Inspect `.prereview/comments.csv`. The header is 12 columns ending
   in `area`. Your two new rows have `kind=area`, `from_line=0`, and
   the `area` column carries the JSON blob with each value in
   `[0, 1]`.
6. (Phase-1 reminder.) The toolbar "Comment on file" button still
   works — file-level comments and area comments live in the same
   list above the image. Each kind shows its own badge: `file` vs
   `area`.

## Coordinate contract for the skill

When a downstream LLM consumes the CSV, area rows tell it "the human
flagged this rectangle". Coords are 0..1 fractions of the image's
natural dimensions:

- The browser doesn't need to know the natural size to render the
  overlay — fractions of the rendered rect == fractions of the
  natural rect under uniform scale.
- The LLM converts to pixels (if it wants them) by reading the
  natural dimensions from the file itself.
- Re-encoding the image at different pixel dimensions still
  highlights the same logical region.

See `skill/SKILL.md` (the "Comment kinds" subsection) for the formal
contract.
