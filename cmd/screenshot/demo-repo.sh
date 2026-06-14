#!/usr/bin/env bash
# Populate a throwaway demo git repo with varied, realistic content for the
# README screenshots: a code file with a working-tree diff, a Markdown file with
# headings + a relative link, and an image (for area annotations). Deterministic
# so screenshots stay stable across refreshes.
#
# Usage: demo-repo.sh <target-dir> <image-source.png>
set -euo pipefail

dir="$1"
img_src="$2"

mkdir -p "$dir"
cd "$dir"

git init -q -b main
git config user.email demo@example.com
git config user.name "Demo"
git config commit.gpgsign false

# Keep prereview's own working dir out of the reviewed file list — via a local
# exclude (not a committed .gitignore), so the file list stays exactly the demo
# content with nothing extra.
printf '.prereview/\n' >> .git/info/exclude

# --- committed base versions -------------------------------------------------
cat > payment.go <<'GO'
package payment

import "errors"

// Charge captures a payment for an order. Amount is in minor units (cents).
func Charge(orderID string, cents int64) error {
	if cents <= 0 {
		return errors.New("amount must be positive")
	}
	if orderID == "" {
		return errors.New("missing order id")
	}
	return gateway.Submit(orderID, cents)
}

// Refund reverses a prior capture in full.
func Refund(orderID string) error {
	return gateway.Reverse(orderID)
}
GO

cat > guide.md <<'MD'
# Payment Guide

How charges and refunds flow through the service.

## Charging

A charge validates the amount and order, then submits to the gateway.
See [the implementation](payment.go) for details.

### Retries

Transient gateway errors are retried with backoff.

## Refunds

Refunds reverse a capture in full. Partial refunds are not supported yet.
MD

git add -A
git commit -q -m "seed payment service"

# architecture.png arrives as an untracked working-tree change so it appears
# in the (default) changed-files list, not just under "show all".
cp "$img_src" architecture.png

# --- working-tree changes (these become the diff under review) ---------------
# A single contiguous edit to Charge (retry loop) so the diff is one hunk —
# keeps inline comments from rendering against two overlapping hunks. Refund is
# left exactly as committed.
cat > payment.go <<'GO'
package payment

import "errors"

// Charge captures a payment for an order. Amount is in minor units (cents).
func Charge(orderID string, cents int64) error {
	if cents <= 0 {
		return errors.New("amount must be positive")
	}
	if orderID == "" {
		return errors.New("missing order id")
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := gateway.Submit(orderID, cents); err == nil {
			return nil
		}
	}
	return errors.New("charge failed after retries")
}

// Refund reverses a prior capture in full.
func Refund(orderID string) error {
	return gateway.Reverse(orderID)
}
GO

# Tweak the guide so it shows as modified too.
printf '\n## Limits\n\nMaximum charge is 100,000 cents per order.\n' >> guide.md
