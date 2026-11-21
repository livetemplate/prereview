package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func pidFile(dir string) string { return filepath.Join(dir, "server.pid") }

// deadPID returns a pid that is (almost certainly) not alive. It spawns nothing
// and signals nothing real — it just picks a very high number unlikely to be in
// use, then confirms it's dead via processAlive so the test is self-checking.
func deadPID(t *testing.T) int {
	t.Helper()
	for _, pid := range []int{99999999, 88888888, 77777777} {
		if !processAlive(pid) {
			return pid
		}
	}
	t.Fatal("could not find a definitely-dead pid")
	return 0
}

func TestClaimServerLock_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	release, err := claimServerLock(dir, false)
	if err != nil {
		t.Fatalf("claim on empty dir: %v", err)
	}
	if _, err := os.Stat(pidFile(dir)); err != nil {
		t.Fatalf("pid file not written: %v", err)
	}
	release()
	if _, err := os.Stat(pidFile(dir)); !os.IsNotExist(err) {
		t.Fatalf("release should have removed the pid file, stat err = %v", err)
	}
}

func TestClaimServerLock_DeadPIDReclaims(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(pidFile(dir), []byte(strconv.Itoa(deadPID(t))), 0o644); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}
	release, err := claimServerLock(dir, false)
	if err != nil {
		t.Fatalf("claim over a dead pid should succeed: %v", err)
	}
	defer release()
	if got, _ := readServerPID(pidFile(dir)); got != os.Getpid() {
		t.Fatalf("pid file = %d, want our pid %d", got, os.Getpid())
	}
}

func TestClaimServerLock_AlivePIDNoReplaceErrors(t *testing.T) {
	dir := t.TempDir()
	// os.Getpid() is alive (it's us), so a non-replacing claim must refuse.
	if err := os.WriteFile(pidFile(dir), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}
	release, err := claimServerLock(dir, false)
	if err == nil {
		release()
		t.Fatal("expected an error claiming over a live server without --replace")
	}
}

func TestClaimServerLock_ReleaseLeavesForeignPID(t *testing.T) {
	dir := t.TempDir()
	release, err := claimServerLock(dir, false)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Simulate a later server replacing us: it overwrote server.pid with its
	// own (different) pid. Our deferred release must NOT strip its lock.
	foreign := deadPID(t)
	if err := os.WriteFile(pidFile(dir), []byte(strconv.Itoa(foreign)), 0o644); err != nil {
		t.Fatalf("overwrite pid file: %v", err)
	}
	release()
	data, err := os.ReadFile(pidFile(dir))
	if err != nil {
		t.Fatalf("pid file should still exist: %v", err)
	}
	if got := string(data); got != strconv.Itoa(foreign) {
		t.Fatalf("pid file = %q, want the foreign pid %d preserved", got, foreign)
	}
}

// sleeper starts a long-lived child that is alive but is not a prereview server,
// the shape of a recycled pid. The cleanup kills it if the test didn't.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	return startChild(t, exec.Command("sleep", "30"))
}

// startChild starts cmd and reaps it when it exits, so a stopped child doesn't
// linger as a zombie that processAlive still sees. The cleanup kills it if the
// test didn't.
func startChild(t *testing.T, cmd *exec.Cmd) *exec.Cmd {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", cmd.Path, err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return cmd
}

// pidServer answers /_server-pid the way a running prereview does, with pid.
func pidServer(t *testing.T, pid int) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(serverPIDRoute, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strconv.Itoa(pid))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func seedRecord(t *testing.T, dir string, rec serverRecord) {
	t.Helper()
	if err := writeServerRecord(pidFile(dir), dir, rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}
}

// --replace must not signal a live pid it can't confirm is this store's server.
func TestClaimServerLock_ReplaceRefusesUnconfirmed(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string, pid int){
		"legacy plain pid": func(t *testing.T, dir string, pid int) {
			if err := os.WriteFile(pidFile(dir), []byte(strconv.Itoa(pid)), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"no address yet": func(t *testing.T, dir string, pid int) {
			seedRecord(t, dir, serverRecord{PID: pid})
		},
		"nothing listening": func(t *testing.T, dir string, pid int) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			ln.Close()
			seedRecord(t, dir, serverRecord{PID: pid, Addr: addr})
		},
		"another server's pid": func(t *testing.T, dir string, pid int) {
			seedRecord(t, dir, serverRecord{PID: pid, Addr: pidServer(t, pid+1)})
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			child := sleeper(t)
			seed(t, dir, child.Process.Pid)
			release, err := claimServerLock(dir, true)
			if err == nil {
				release()
				t.Fatal("--replace claimed over an unconfirmed pid")
			}
			if !processAlive(child.Process.Pid) {
				t.Fatal("--replace signalled a pid it could not confirm")
			}
			if pid, _ := readServerPID(pidFile(dir)); pid != child.Process.Pid {
				t.Fatalf("server.pid = %d, want the untouched %d", pid, child.Process.Pid)
			}
		})
	}
}

// A confirmed server is stopped and its lock taken over.
func TestClaimServerLock_ReplaceStopsConfirmed(t *testing.T) {
	dir := t.TempDir()
	child := sleeper(t)
	seedRecord(t, dir, serverRecord{PID: child.Process.Pid, Addr: pidServer(t, child.Process.Pid)})
	release, err := claimServerLock(dir, true)
	if err != nil {
		t.Fatalf("--replace over a confirmed server: %v", err)
	}
	defer release()
	if processAlive(child.Process.Pid) {
		t.Fatal("the confirmed server was not stopped")
	}
	if pid, _ := readServerPID(pidFile(dir)); pid != os.Getpid() {
		t.Fatalf("server.pid = %d, want ours %d", pid, os.Getpid())
	}
}

// recordServerAddr adds the listen address to our own record, and an
// unspecified bind address is reached over loopback.
func TestRecordServerAddr(t *testing.T) {
	dir := t.TempDir()
	release, err := claimServerLock(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := recordServerAddr(dir, "", &net.TCPAddr{IP: net.IPv4zero, Port: 4799}); err != nil {
		t.Fatal(err)
	}
	rec, ok := readServerRecord(pidFile(dir))
	if !ok || rec.PID != os.Getpid() || rec.Addr != "127.0.0.1:4799" {
		t.Fatalf("record = %+v, %v; want our pid at 127.0.0.1:4799", rec, ok)
	}
}

// TestStopServer_KillsAfterGrace pins the --replace fallback: a server that
// outlives its graceful shutdown is killed rather than reported stuck.
func TestStopServer_KillsAfterGrace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell to ignore SIGTERM")
	}
	defer func(g, k time.Duration) { stopGrace, stopKillWait = g, k }(stopGrace, stopKillWait)
	stopGrace, stopKillWait = 300*time.Millisecond, 2*time.Second

	// An ignored signal stays ignored across exec, so sleep shrugs off SIGTERM.
	// "ready" says the trap is in place before the test signals.
	cmd := exec.Command("sh", "-c", `trap "" TERM; echo ready; exec sleep 30`)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	startChild(t, cmd)
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "ready\n" {
		t.Fatalf("child said %q", line)
	}

	start := time.Now()
	if err := stopServer(cmd.Process.Pid); err != nil {
		t.Fatalf("stopServer: %v", err)
	}
	if took := time.Since(start); took < stopGrace {
		t.Errorf("killed after %v, before the %v grace", took, stopGrace)
	}
}
