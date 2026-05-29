# File-level comments fixture

This README is the text-file half of the issue #16 phase-1 fixture. The
matching `logo.png` next to it is a 1×1 pixel PNG used to exercise the
binary-file path (where line-anchored comments are impossible).

## Why this fixture exists

Issue #16's premise is that **line-anchored comments leave gaps**:

- Binary files (images, PDFs, video, audio) render as a `<img>` or
  `<embed>` with no clickable lines.
- "File too large to review" placeholders have nothing to anchor to.
- General feedback like "rename this", "wrong file in the PR", or
  "good test coverage" doesn't belong on any specific line.

The new "Comment on file" toolbar button (and the matching mobile
menu item) opens a composer that bypasses the line-selection
machinery and persists a CSV row with `kind=file`, `from_line=0`,
`to_line=0`.

## How to drive prereview against this fixture by hand

```sh
mkdir /tmp/fc && cd /tmp/fc && git init -q -b main \
  && git config user.email t@t && git config user.name t \
  && git config commit.gpgsign false
cp <PREREVIEW>/testdata/filecomments/* .
git add -A && git commit -q -m seed
# Add a change so the diff isn't empty:
echo $'\nA paragraph added after seeding.' >> README.md
prereview --repo $(pwd)
```

Then in the browser:

1. Click **README.md** in the sidebar. Click **"Comment on file"** in
   the toolbar (top-right). Type a body. Save. The comment renders
   above the markdown body with a `file` badge instead of `L4-L6`.
2. Click **logo.png** in the sidebar — the image renders inline.
   Click **"Comment on file"** again. The exact same composer
   appears above the image preview. Save. This is the path the issue
   explicitly called out as missing.
3. Inspect `.prereview/comments.csv` and confirm the 11-column header
   ends with `kind` and your file-level rows have
   `from_line,to_line,side,anchor,anchor_status = 0,0,,,` and
   `kind = file`.
