package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// serverPIDFileName is the per-store lock file. Because it lives INSIDE the store,
// keying the store directory by the reviewed target (storeDirFor) is what makes the
// lock per-target rather than per-directory (#199).
const serverPIDFileName = "server.pid"

// serverPIDRoute answers with the serving process's pid. --replace asks it at the
// address in server.pid before it signals anything: a pid alone may have been
// recycled by an unrelated process since the record was written.
const serverPIDRoute = "/_server-pid"

// serverRecord is server.pid's content. Addr is empty from the claim until the
// server is listening. A file holding a bare pid predates the record and has no
// address to confirm against.
type serverRecord struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr,omitempty"`
	// Repo is the directory the review's files are relative to (REPO), so an
	// agent verb can check a path against it; "" for a live-site review.
	Repo string `json:"repo,omitempty"`
}

// claimServerLock records that THIS process owns the review server for the store
// under prereviewDir (via prereviewDir/server.pid), refusing to start a second
// server for the same store — two servers fighting over one .prereview/ corrupt
// the queue. If a live server already holds the lock: with replace it is stopped
// (SIGTERM, then Kill) and taken over, once confirmServer shows the pid is that
// server; without replace an error is returned. The
// returned release removes the pid file only if it still holds our pid, so a
// server that later replaced us keeps its lock when our deferred release runs.
func claimServerLock(prereviewDir string, replace bool) (release func(), err error) {
	if err := os.MkdirAll(prereviewDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", prereviewDir, err)
	}
	pidPath := filepath.Join(prereviewDir, serverPIDFileName)

	if rec, ok := readServerRecord(pidPath); ok && processAlive(rec.PID) {
		if !replace {
			return nil, fmt.Errorf("prereview is already running for this store (pid %d); pass --replace to take over", rec.PID)
		}
		if !confirmServer(rec) {
			return nil, fmt.Errorf("--replace: can't confirm pid %d is the prereview server for this store, so it was not stopped; stop that server yourself, or delete %s if it has exited", rec.PID, pidPath)
		}
		if err := stopServer(rec.PID); err != nil {
			return nil, err
		}
	}

	if err := writeServerRecord(pidPath, prereviewDir, serverRecord{PID: os.Getpid()}); err != nil {
		return nil, err
	}

	return func() {
		// Only remove the lock if it's still ours. A server that replaced us
		// (after we exited) now owns server.pid; deleting it would strip its
		// lock and let a third server start alongside it.
		if pid, ok := readServerPID(pidPath); ok && pid == os.Getpid() {
			_ = os.Remove(pidPath)
		}
	}, nil
}

// stopServer terminates the running server pid and waits for it to exit, so the
// replacement can claim the store cleanly. SIGTERM lets it unwind its deferred
// cleanups (comments auto-save); Kill is the fallback if signalling fails.
func stopServer(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find running server (pid %d): %w", pid, err)
	}
	if serr := p.Signal(syscall.SIGTERM); serr != nil {
		_ = p.Kill()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		return fmt.Errorf("existing prereview server (pid %d) did not exit after --replace", pid)
	}
	return nil
}

// recordServerAddr adds this server's listen address to the store's server.pid,
// once the listener exists, so a later --replace can confirm it. An unspecified
// bind address (0.0.0.0, ::) is recorded as loopback, the address a local
// --replace can dial.
func recordServerAddr(prereviewDir, repo string, addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("record server address: not TCP: %v", addr)
	}
	ip := tcp.IP
	if ip == nil || ip.IsUnspecified() {
		ip = net.IPv4(127, 0, 0, 1)
	}
	rec := serverRecord{PID: os.Getpid(), Addr: net.JoinHostPort(ip.String(), strconv.Itoa(tcp.Port)), Repo: repo}
	return writeServerRecord(filepath.Join(prereviewDir, serverPIDFileName), prereviewDir, rec)
}

// confirmServer reports whether the server at rec.Addr answers serverPIDRoute
// with rec.PID, which makes rec.PID the process that wrote the record.
func confirmServer(rec serverRecord) bool {
	if rec.Addr == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + rec.Addr + serverPIDRoute)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32))
	return err == nil && resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == strconv.Itoa(rec.PID)
}

// serveServerPID answers serverPIDRoute for this process.
func serveServerPID(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, strconv.Itoa(os.Getpid()))
}

// writeServerRecord writes rec to pidPath atomically: a temp file in the same dir
// renamed over the target, so a reader never sees a half-written record.
func writeServerRecord(pidPath, dir string, rec serverRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode pid record: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "server.pid-*")
	if err != nil {
		return fmt.Errorf("create pid temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write pid temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close pid temp: %w", err)
	}
	if err := os.Rename(tmpName, pidPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install pid file: %w", err)
	}
	return nil
}

// processAlive reports whether pid names a live process, via signal 0 (which
// checks for existence/permission without delivering a signal).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// readServerRecord reads a server.pid file. A bare pid (the form before the
// record) reads as a record with no address.
func readServerRecord(path string) (serverRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return serverRecord{}, false
	}
	var rec serverRecord
	if json.Unmarshal(data, &rec) == nil && rec.PID > 0 {
		return rec, true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return serverRecord{PID: pid}, err == nil
}

// readServerPID reads a server.pid file's pid.
func readServerPID(path string) (int, bool) {
	rec, ok := readServerRecord(path)
	return rec.PID, ok
}
