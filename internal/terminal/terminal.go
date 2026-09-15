package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// closeGracePeriod is how long a shell may take to exit after SIGTERM before it
// is killed outright. Interactive shells ignore SIGTERM by default, so waiting
// without a bound would block the caller forever.
const closeGracePeriod = 500 * time.Millisecond

type Terminal struct {
	process *exec.Cmd
	file    *os.File
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

// Close terminates the whole PTY process group and always returns. The shell is
// asked to exit with SIGTERM, then killed if it does not comply within the grace
// period, because an interactive shell ignores SIGTERM. Callers may be serving a
// request, so this must never block indefinitely.
func (terminal *Terminal) Close() error {
	// Closing the master makes the shell see EOF and unblocks the reader.
	_ = terminal.file.Close()
	if terminal.process.Process == nil {
		return nil
	}
	processGroup := -terminal.process.Process.Pid
	_ = syscall.Kill(processGroup, syscall.SIGTERM)

	exited := make(chan error, 1)
	go func() { exited <- terminal.process.Wait() }()

	select {
	case err := <-exited:
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	case <-time.After(closeGracePeriod):
	}
	_ = syscall.Kill(processGroup, syscall.SIGKILL)
	if err := <-exited; err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
