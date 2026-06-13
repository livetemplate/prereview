#!/bin/sh
# prereview installer — downloads the latest release binary for your
# platform, verifies its checksum, and drops it on your PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/livetemplate/prereview/main/install.sh | sh
#
# Knobs (environment variables):
#   PREREVIEW_VERSION      install a specific tag (e.g. v0.3.6) instead of latest
#   PREREVIEW_INSTALL_DIR  install into this dir instead of the auto-picked one
#
# Requires: curl or wget, tar, uname, and sha256sum or shasum.
# Runtime prerequisite: git must be on your PATH for prereview to work.
# Uninstall later with `prereview --uninstall` (removes only the binary;
# your review comments in each repo's .prereview/ are left untouched).
set -eu

OWNER="livetemplate"
REPO="prereview"
BIN="prereview"

err() { printf 'install: %s\n' "$1" >&2; exit 1; }
info() { printf '%s\n' "$1" >&2; }

# --- pick a downloader -----------------------------------------------------
# Emits the fetched body on stdout. We support both curl and wget so the
# one-liner works on minimal images that ship only one of them.
if command -v curl >/dev/null 2>&1; then
	dl() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	dl() { wget -qO- "$1"; }
else
	err "need curl or wget installed"
fi

# --- detect platform -------------------------------------------------------
# These must match goreleaser's archive name template {{.Os}}_{{.Arch}}.
os=$(uname -s)
case "$os" in
	Linux) os="linux" ;;
	Darwin) os="darwin" ;;
	*) err "unsupported OS '$os'. On Windows use Scoop (see the README); otherwise download an archive from https://github.com/$OWNER/$REPO/releases" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch="amd64" ;;
	aarch64 | arm64) arch="arm64" ;;
	*) err "unsupported architecture '$arch'. Download an archive from https://github.com/$OWNER/$REPO/releases" ;;
esac

# --- resolve version -------------------------------------------------------
tag="${PREREVIEW_VERSION:-}"
if [ -z "$tag" ]; then
	info "Resolving latest release…"
	# Parse "tag_name": "v0.3.6" from the GitHub API without jq. Buffer the
	# whole response first so grep's early -m1 exit can't SIGPIPE the
	# downloader ("curl: Failed writing body").
	latest=$(dl "https://api.github.com/repos/$OWNER/$REPO/releases/latest") ||
		err "could not reach the GitHub releases API"
	tag=$(printf '%s' "$latest" |
		grep -m1 '"tag_name"' |
		sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
	[ -n "$tag" ] || err "could not determine the latest release tag"
fi

# goreleaser strips the leading v from the archive name (tag v0.3.6 ->
# prereview_0.3.6_...), but the release URL still uses the v-prefixed tag.
version=${tag#v}
archive="${BIN}_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$OWNER/$REPO/releases/download/$tag"

info "Installing $BIN $tag ($os/$arch)…"

# --- download into a temp dir ----------------------------------------------
tmp=$(mktemp -d 2>/dev/null || mktemp -d -t prereview)
trap 'rm -rf "$tmp"' EXIT INT TERM

dl "$base_url/$archive" >"$tmp/$archive" ||
	err "download failed: $base_url/$archive (is $archive a real asset for $tag?)"
dl "$base_url/checksums.txt" >"$tmp/checksums.txt" ||
	err "could not download checksums.txt for $tag"

# --- verify checksum -------------------------------------------------------
# checksums.txt lists "<sha256>  <archive-name>" per line. Pull the line for
# our archive and check it with whichever tool the platform provides.
want=$(grep " $archive\$" "$tmp/checksums.txt" | awk '{print $1}')
[ -n "$want" ] || err "no checksum entry for $archive in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
else
	err "need sha256sum or shasum to verify the download"
fi
[ "$want" = "$got" ] || err "checksum mismatch for $archive (expected $want, got $got)"

# --- extract ---------------------------------------------------------------
tar -xzf "$tmp/$archive" -C "$tmp" "$BIN" ||
	err "could not extract $BIN from $archive"

# --- choose an install dir -------------------------------------------------
# Honour an explicit override; otherwise prefer a system dir if we can write
# it, falling back to a per-user dir that needs no sudo.
if [ -n "${PREREVIEW_INSTALL_DIR:-}" ]; then
	dir="$PREREVIEW_INSTALL_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
	dir="/usr/local/bin"
else
	dir="$HOME/.local/bin"
fi
mkdir -p "$dir" || err "cannot create $dir"

install -m 0755 "$tmp/$BIN" "$dir/$BIN" 2>/dev/null ||
	{ cp "$tmp/$BIN" "$dir/$BIN" && chmod 0755 "$dir/$BIN"; } ||
	err "could not install to $dir (set PREREVIEW_INSTALL_DIR to a writable dir)"

info "Installed $BIN $tag → $dir/$BIN"

# --- post-install hints ----------------------------------------------------
case ":$PATH:" in
	*":$dir:"*) ;;
	*) info "Note: $dir is not on your PATH. Add it, e.g.:"
	   info "  export PATH=\"$dir:\$PATH\"" ;;
esac

command -v git >/dev/null 2>&1 ||
	info "Note: prereview needs git on your PATH at runtime; install git to use it."

info "Run '$BIN --version' to confirm, then '$BIN' inside a git repo."
