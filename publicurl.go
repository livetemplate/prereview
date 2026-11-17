package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// resolvePublicURL checks --public-url / PREREVIEW_PUBLIC_URL (#65): the address
// a browser outside the box uses, such as a sandbox proxy's
// https://vm.exe.xyz:4799. It returns the URL without a trailing slash and the
// port to bind: --port when given, else the URL's own port, so the proxy's
// port and the bind port match with one flag. A URL with no port of its own
// (a proxy on 443) needs --port, since a random port can't be proxied.
// external refuses the combination: --external serves the live site from a
// second, random port that a one-port proxy can't reach.
func resolvePublicURL(raw string, port int, portSet, external bool) (string, int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", port, nil
	}
	if external {
		return "", 0, fmt.Errorf("--public-url doesn't work with --external: the live site is served from a second, random port that a one-port proxy can't reach")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", 0, fmt.Errorf("--public-url %q: want an http(s) URL, e.g. https://vm.exe.xyz:4799", raw)
	}
	if !portSet {
		port, _ = strconv.Atoi(u.Port())
	}
	if port <= 0 {
		return "", 0, fmt.Errorf("--public-url %q needs a fixed port to bind: pass --port with the port the proxy forwards to", raw)
	}
	return strings.TrimRight(u.String(), "/"), port, nil
}
