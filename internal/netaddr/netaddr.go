// Package netaddr resolves which host/IP the review server should bind to
// and which alternate URLs to advertise, with first-class support for
// Tailscale tailnets so a remote box is reachable from the user's phone
// without exposing the diff on a public interface.
package netaddr

// Reachability resolution for the review server.
//
// Default bind is 127.0.0.1 (localhost-only) — perfect on a dev laptop,
// useless on a remote box where the human reviews from a phone. The
// previous workaround was telling the operator to pass --host 0.0.0.0,
// which exposes the source diff on EVERY interface, including a public
// IP. Tailscale gives us a better answer: a stable, authenticated
// 100.64.0.0/10 address reachable only from the user's own tailnet.
//
// When prereview detects it's on a remote box and no explicit --host was
// given, it binds to that Tailscale address instead of loopback:
// reachable from the phone, never exposed to the public internet.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// tailscaleCGNAT is the 100.64.0.0/10 carrier-grade-NAT block Tailscale
// draws every node's IPv4 from (RFC 6598). Membership in this range on a
// local interface is a reliable, dependency-free "this host is on a
// tailnet" signal — no `tailscale` binary required.
var tailscaleCGNAT = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// TailscaleIPv4 returns this host's Tailscale IPv4 address and, when
// discoverable, its MagicDNS hostname (trailing dot stripped). Either
// may be empty:
//
//   - ip == ""        → no tailnet interface on this host
//   - magicDNS == ""  → on a tailnet, but the `tailscale` CLI wasn't
//     available to resolve the friendly name; the IP still works
//
// The IP comes from pure interface enumeration (always works, headless,
// testable). The MagicDNS name is a best-effort nicety shelled out to
// `tailscale status --json` — any failure there is swallowed, never
// fatal: a working 100.x URL beats a missing one.
func TailscaleIPv4() (ip string, magicDNS string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipnet.IP.To4()
			if v4 != nil && tailscaleCGNAT.Contains(v4) {
				ip = v4.String()
				break
			}
		}
		if ip != "" {
			break
		}
	}
	if ip == "" {
		return "", ""
	}
	return ip, magicDNSName()
}

// magicDNSName best-effort resolves this node's MagicDNS hostname via the
// tailscale CLI. Returns "" on any error (CLI absent, not logged in,
// JSON shape changed) — the caller already has a usable IP.
func magicDNSName() string {
	// Hard runtime bound: this runs in the server startup path, so a
	// wedged tailscaled must never hang prereview booting. Only
	// exec.CommandContext actually cancels (SIGKILLs) the child on
	// timeout — Cmd.WaitDelay does NOT bound runtime (it's merely the
	// post-cancel grace before the pipes are force-closed), which is the
	// trap this deliberately avoids.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var status struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return ""
	}
	return strings.TrimSuffix(status.Self.DNSName, ".")
}

// IsRemoteBox reports whether prereview is running on a machine the user
// is connected to over the network rather than sitting in front of. An
// active SSH session is the canonical, dependency-free signal: any of
// the three vars is set by sshd for the session's processes.
//
// This gates the auto-rebind: on a local dev box (no SSH) we keep the
// historical 127.0.0.1 default so nothing about the laptop workflow
// changes, even if that laptop happens to be on a tailnet.
func IsRemoteBox() bool {
	for _, v := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

// ResolveBindHost decides which host/IP the server should bind to, given
// the situation. It returns the bind host plus an optional human warning
// (printed to stderr by the caller; "" means nothing to say).
//
// === The contract this MUST satisfy (see netaddr_test.go) ===
//
//	explicitHost == true
//	    → host, ""            (the operator's --host is absolute,
//	                           even if it's 0.0.0.0 — their call)
//	!explicitHost && remote && tsIP != ""
//	    → tsIP, ""            (the whole point: reachable over the
//	                           tailnet, never the public internet)
//	!explicitHost && remote && tsIP == ""
//	    → "127.0.0.1", <warn> (remote but NO tailnet — loopback is
//	                           unreachable from the user's phone;
//	                           the warning must tell them so AND how
//	                           to fix it, e.g. mention --host)
//	!explicitHost && !remote
//	    → "127.0.0.1", ""     (local dev — unchanged historical default)
//
// Parameters:
//
//	explicitHost — did the operator actually pass --host on the CLI?
//	host         — the value of --host (its flag default is "127.0.0.1")
//	remote       — IsRemoteBox()
//	tsIP         — TailscaleIPv4()'s ip ("" when there's no tailnet)
//
// ─────────────────────────────────────────────────────────────────────
// TODO(you): implement this. ~5–10 lines. The four happy/edge rows above
// are the spec; netaddr_test.go pins them as a table. The judgment call
// is the third row — remote with no tailnet: we fall back to loopback
// (per the chosen policy) but the *warning text* is the difference
// between a user who's stuck and one who knows to pass --host. Make that
// message count; it's the only feedback they'll get before they go
// looking for a URL that isn't reachable.
// ─────────────────────────────────────────────────────────────────────
func ResolveBindHost(explicitHost bool, host string, remote bool, tsIP string) (bindHost string, warn string) {
	// An explicit --host is an absolute operator override: honored
	// verbatim (even 0.0.0.0 — their call), never auto-rebound.
	if explicitHost {
		return host, ""
	}
	// Local dev: unchanged historical default, even on a tailnet.
	if !remote {
		return "127.0.0.1", ""
	}
	// Remote box with a tailnet — the whole point: reachable from the
	// user's phone over Tailscale, never the public internet.
	if tsIP != "" {
		return tsIP, ""
	}
	// Remote box, NO tailnet. Loopback is the only safe default but is
	// unreachable from the user's phone, and this string is the only
	// feedback they get before chasing a URL that never loads — so it
	// names where they are, why it won't work, and the literal fix.
	return "127.0.0.1", "remote box has no tailnet — bound 127.0.0.1, which your phone can't reach; pass --host <private-ip> (e.g. a LAN or VPN address) to bind a reachable interface"
}

// AltURLs returns *additional* reachable URLs to advertise alongside the
// canonical READY line — never including the bind URL itself (the caller
// already prints that). The MagicDNS hostname is the headline extra: on
// a phone, tapping `http://box.tailnet.ts.net:PORT` beats typing a
// 100.x.y.z octet string. Order is stable and the bound form is
// de-duplicated out so the user never sees the same endpoint twice.
func AltURLs(bindHost, tsIP, magicDNS string, port int) []string {
	seen := map[string]bool{bindHost: true}
	var out []string
	add := func(h string) {
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, fmt.Sprintf("http://%s:%d", h, port))
	}
	// Hostname first — it's the one a human actually wants to tap.
	add(magicDNS)
	add(tsIP)
	return out
}
