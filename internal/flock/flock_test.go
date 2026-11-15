package flock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Two holders never overlap: each increments a counter file read-modify-write
// under the lock; without mutual exclusion increments would be lost.
func TestLockSerializes(t *testing.T) {
	dir := t.TempDir()
	lock, counter := filepath.Join(dir, "x.lock"), filepath.Join(dir, "n")
	os.WriteFile(counter, []byte("0"), 0o644)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				unlock, err := Lock(lock)
				if err != nil {
					t.Error(err)
					return
				}
				b, _ := os.ReadFile(counter)
				n, _ := strconv.Atoi(string(b))
				time.Sleep(50 * time.Microsecond)
				os.WriteFile(counter, []byte(strconv.Itoa(n+1)), 0o644)
				unlock()
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(counter)
	if string(b) != "200" {
		t.Errorf("counter = %s, want 200 (lost updates under the lock)", b)
	}
}

// The lock holds across processes, which is the point (one server per review).
func TestLockAcrossProcesses(t *testing.T) {
	if os.Getenv("FLOCK_CHILD") != "" {
		unlock, err := Lock(os.Getenv("FLOCK_CHILD"))
		if err != nil {
			os.Exit(2)
		}
		os.WriteFile(os.Getenv("FLOCK_CHILD")+".held", nil, 0o644)
		time.Sleep(400 * time.Millisecond)
		unlock()
		os.Exit(0)
	}
	lock := filepath.Join(t.TempDir(), "x.lock")
	cmd := exec.Command(os.Args[0], "-test.run", "TestLockAcrossProcesses")
	cmd.Env = append(os.Environ(), "FLOCK_CHILD="+lock)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ { // wait for the child to hold it
		if _, err := os.Stat(lock + ".held"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	unlock, err := Lock(lock)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Errorf("took the lock after %s while another process held it", waited)
	}
	cmd.Wait()
}
