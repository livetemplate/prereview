#!/usr/bin/env bash
set -euo pipefail

# prereview Release Script
# Usage: ./release.sh <version>
# Example: ./release.sh 0.0.1
#
# Creates a v<version> git tag and pushes it. The tag triggers
# .github/workflows/release.yml, which runs goreleaser to publish the
# GitHub Release + cross-platform archives.
#
# Project rule: releases MUST go through this script. Never `git tag`
# by hand.

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

usage() {
    echo "Usage: $0 <version>"
    echo ""
    echo "Creates a new release by tagging the repository."
    echo "The tag triggers .github/workflows/release.yml (goreleaser)."
    echo ""
    echo "Examples:"
    echo "  $0 0.0.1    # Creates tag v0.0.1"
    echo "  $0 0.1.0    # Creates tag v0.1.0"
    exit 1
}

if [ $# -ne 1 ]; then
    usage
fi

VERSION="$1"

# Validate version format (semver without v prefix)
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
    echo -e "${RED}Error: Invalid version format. Expected: X.Y.Z or X.Y.Z-suffix${NC}"
    echo "Examples: 0.0.1, 0.1.0, 1.0.0-beta.1"
    exit 1
fi

TAG="v${VERSION}"

# Refuse on uncommitted changes
if ! git diff-index --quiet HEAD --; then
    echo -e "${RED}Error: You have uncommitted changes. Commit or stash them first.${NC}"
    git status --short
    exit 1
fi

# Warn if not on main
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
if [ "$CURRENT_BRANCH" != "main" ]; then
    echo -e "${YELLOW}Warning: You are on branch '$CURRENT_BRANCH', not 'main'.${NC}"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Refuse if tag already exists
if git rev-parse "$TAG" >/dev/null 2>&1; then
    echo -e "${RED}Error: Tag $TAG already exists.${NC}"
    exit 1
fi

echo -e "${YELLOW}Fetching latest from origin...${NC}"
git fetch origin

# Warn if local diverges from origin/main
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git rev-parse origin/main 2>/dev/null || echo "")
if [ -n "$REMOTE" ] && [ "$LOCAL" != "$REMOTE" ]; then
    echo -e "${YELLOW}Warning: Local branch differs from origin/main.${NC}"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

echo ""
echo -e "${GREEN}Release Summary:${NC}"
echo "  Version: $VERSION"
echo "  Tag:     $TAG"
echo "  Commit:  $(git rev-parse --short HEAD)"
echo "  Message: $(git log -1 --pretty=%s)"
echo ""

read -p "Create and push tag $TAG? (y/N) " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 1
fi

echo -e "${YELLOW}Creating tag $TAG...${NC}"
git tag -a "$TAG" -m "Release $VERSION"

echo -e "${YELLOW}Pushing tag to origin...${NC}"
git push origin "$TAG"

echo ""
echo -e "${GREEN}Success! Tag $TAG has been pushed.${NC}"
echo ""
echo "The release workflow will now run goreleaser automatically."
echo "Monitor: https://github.com/livetemplate/prereview/actions"
echo "Release: https://github.com/livetemplate/prereview/releases/tag/$TAG"
