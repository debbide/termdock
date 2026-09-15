package terminal

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests allocate real PTYs and spawn real shells, so they only run on the
// Unix targets the package is built for. They exist because the interface-level
// tests in internal/server use a fake terminal, which can never reproduce a
// shell that ignores SIGTERM.

func shellPath(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"/bin/bash", "/bin/sh"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("no shell available")
	return ""
}

func TestStartAndEchoOutput(t *testing.T) {
	shell := shellPath(t)
	session, err := Start(shell, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("echo terminal-integration-marker\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	buffer := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	seen := ""
	for time.Now().Before(deadline) {
		count, err := session.Read(buffer)
		if count > 0 {
			seen += string(buffer[:count])
			if strings.Contains(seen, "terminal-integration-marker") {
				return
			}
		}
		if err != nil {
			t.Fatalf("Read: %v (output so far: %q)", err, seen)
		}
	}
	t.Fatalf("marker never appeared in PTY output: %q", seen)
}

func TestResizeRejectsInvalidDimensions(t *testing.T) {
	shell := shellPath(t)
	session, err := Start(shell, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer session.Close()

	for _, size := range [][2]uint16{{0, 24}, {80, 0}, {1, 24}, {80, 1}, {1001, 24}, {80, 1001}} {
		if err := session.Resize(size[0], size[1]); err == nil {
			t.Errorf("Resize(%d, %d) = nil, want error", size[0], size[1])
		}
	}
	if err := session.Resize(120, 40); err != nil {
		t.Errorf("Resize(120, 40) = %v, want nil", err)
	}
}

// The regression this guards: teardown used to block forever. An interactive
// bash ignores SIGTERM, and while a reader goroutine is blocked in read() the
// master descriptor is not really closed, so the shell never sees SIGHUP and
// never exits. Close must escalate and return promptly anyway.
func TestCloseReturnsAndKillsShellThatIgnoresSigterm(t *testing.T) {
	shell := shellPath(t)
	session, err := Start(shell, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	process := session.process.Process
	if process == nil {
		t.Fatal("Start returned a terminal without a process")
	}

	// Make the shell explicitly ignore SIGTERM and confirm it took effect.
	if _, err := session.Write([]byte("trap '' TERM; echo ready\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForOutput(t, session, "ready")

	// Now reproduce the real runtime condition: the session always has a
	// goroutine blocked in Read, which is what keeps the master descriptor
	// alive and stops the shell from ever seeing SIGHUP.
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			if _, err := session.Read(buffer); err != nil {
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)

	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- session.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close blocked; a shell ignoring SIGTERM must still be killed")
	}
	elapsed := time.Since(started)

	// The shell ignored SIGTERM, so Close must have escalated to SIGKILL. Wait
	// for the kernel to reap it to prove it is really gone.
	reaped := make(chan error, 1)
	go func() { _, err := process.Wait(); reaped <- err }()
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("shell was still running after Close returned")
	}

	if elapsed > closeGracePeriod+5*time.Second {
		t.Errorf("Close took %s, want it bounded by the grace period", elapsed)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	shell := shellPath(t)
	session, err := Start(shell, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := session.Close()
	if first != nil {
		t.Fatalf("first Close: %v", first)
	}
	// A second Close must return promptly rather than block on an already
	// reaped process, and must report the same result.
	done := make(chan error, 1)
	go func() { done <- session.Close() }()
	select {
	case second := <-done:
		if second != nil {
			t.Errorf("second Close: %v, want nil", second)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second Close blocked")
	}
}

// Concurrent closers stand in for the session reader and a logout request both
// ending the same PTY at once.
func TestConcurrentCloseIsSafe(t *testing.T) {
	shell := shellPath(t)
	session, err := Start(shell, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan struct{})
	for worker := 0; worker < 8; worker++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = session.Close()
		}()
	}
	for worker := 0; worker < 8; worker++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent Close blocked")
		}
	}
}

func TestStartFailsForMissingShell(t *testing.T) {
	if _, err := Start("/nonexistent/shell-for-webterm-test", t.TempDir()); err == nil {
		t.Fatal("Start accepted a nonexistent shell")
	}
}

func TestWriteAfterCloseFails(t *testing.T) {
	shell := shellPath(t)
	session, err := Start(shell, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := session.Write([]byte("echo after-close\n")); err == nil {
		t.Fatal("Write succeeded on a closed terminal")
	}
}

func waitForOutput(t *testing.T, session *Terminal, marker string) {
	t.Helper()
	buffer := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	seen := ""
	for time.Now().Before(deadline) {
		count, err := session.Read(buffer)
		if count > 0 {
			seen += string(buffer[:count])
			if strings.Contains(seen, marker) {
				return
			}
		}
		if err != nil {
			t.Fatalf("Read: %v (output so far: %q)", err, seen)
		}
	}
	t.Fatalf("never saw %q in PTY output: %q", marker, seen)
}
