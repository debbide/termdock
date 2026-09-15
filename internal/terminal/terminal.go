package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// closeGracePeriod is how long a shell may take to exit after SIGTERM before it
// is killed outright. Interactive shells ignore SIGTERM by default, so waiting
// without a bound would block the caller forever.
const closeGracePeriod = 500 * time.Millisecond

type Terminal struct {
	process   *exec.Cmd
	file      *os.File
	closeOnce sync.Once
	closeErr  error
}

func Start(shell, workingDirectory string) (*Terminal, error) {
	arguments := []string{}
	if filepath.Base(shell) == "bash" {
		arguments = append(arguments, "--noprofile", "--norc", "-i")
	}
	command := exec.Command(shell, arguments...)
	command.Dir = workingDirectory
	command.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"NO_COLOR=1",
		"PS1=\\u@\\h:\\w# ",
		"PROMPT_COMMAND=",
	)
	file, err := pty.Start(command)
	if err != nil {
		return nil, err
	}
	return &Terminal{process: command, file: file}, nil
}

func (terminal *Terminal) Read(buffer []byte) (int, error)  { return terminal.file.Read(buffer) }
func (terminal *Terminal) Write(buffer []byte) (int, error) { return terminal.file.Write(buffer) }
func (terminal *Terminal) Resize(columns, rows uint16) error {
	if columns < 2 || rows < 2 || columns > 1000 || rows > 1000 {
		return errors.New("invalid terminal size")
	}
	return pty.Setsize(terminal.file, &pty.Winsize{Cols: columns, Rows: rows})
}

// Close terminates the whole PTY process group and always returns. Callers may
// be serving a request, so this must never block indefinitely. Close is
// idempotent: the session that owns the PTY may race with a request ending it.
//
// The escalation to SIGKILL is load-bearing, not belt-and-braces. Two facts
// combine to make the obvious teardown hang: an interactive shell ignores
// SIGTERM, and while the session's reader goroutine is blocked in read() the
// kernel keeps the master descriptor alive (os.File.Close defers the real
// close(2) until the in-flight read returns). The shell therefore never sees
// SIGHUP from the master closing, and waiting for it to exit blocks forever.
// SIGKILL is what actually ends it.
func (terminal *Terminal) Close() error {
	terminal.closeOnce.Do(terminal.close)
	return terminal.closeErr
}

func (terminal *Terminal) close() {
	// Best effort: if no read is in flight this delivers SIGHUP to the shell.
	_ = terminal.file.Close()
	if terminal.process.Process == nil {
		return
	}
	processGroup := -terminal.process.Process.Pid
	_ = syscall.Kill(processGroup, syscall.SIGTERM)

	exited := make(chan error, 1)
	go func() { exited <- terminal.process.Wait() }()

	select {
	case err := <-exited:
		terminal.closeErr = reapError(err)
		return
	case <-time.After(closeGracePeriod):
	}
	_ = syscall.Kill(processGroup, syscall.SIGKILL)
	terminal.closeErr = reapError(<-exited)
}

// reapError reports whether waiting for the shell failed. How the shell died is
// not a teardown failure: a normal exit, SIGHUP from the master closing, and the
// SIGTERM/SIGKILL we send are all expected outcomes.
func reapError(err error) error {
	var exitError *exec.ExitError
	if err == nil || errors.Is(err, io.EOF) || errors.As(err, &exitError) {
		return nil
	}
	return err
}
