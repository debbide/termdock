package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/creack/pty"
)

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
func (terminal *Terminal) Close() error {
	if terminal.process.Process != nil {
		_ = syscall.Kill(-terminal.process.Process.Pid, syscall.SIGTERM)
	}
	_ = terminal.file.Close()
	if err := terminal.process.Wait(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
