package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Status struct {
	Running      bool
	PublicURL    string
	RestartCount int
	Err          error
}

type Manager struct {
	mu     sync.RWMutex
	status Status
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func New() *Manager {
	return &Manager{}
}

func (manager *Manager) Start(ctx context.Context, binary, mode, listen, tokenFile string) (string, error) {
	if mode == "disabled" {
		return "", nil
	}
	arguments, err := commandArguments(mode, listen, tokenFile)
	if err != nil {
		return "", err
	}
	processContext, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(processContext, binary, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := command.StderrPipe()
	if err != nil {
		cancel()
		return "", err
	}
	command.Stdout = command.Stderr
	if err := command.Start(); err != nil {
		cancel()
		return "", err
	}
	manager.mu.Lock()
	manager.cmd = command
	manager.cancel = cancel
	manager.status = Status{Running: true}
	manager.mu.Unlock()

	urlChannel := make(chan string, 1)
	errorChannel := make(chan error, 1)
	go manager.consume(output, urlChannel)
	if mode == "fixed" {
		go manager.superviseFixed(processContext, binary, arguments, command)
		return "", nil
	}
	go func() {
		err := command.Wait()
		manager.mu.Lock()
		manager.status.Running = false
		manager.status.Err = err
		manager.mu.Unlock()
		errorChannel <- err
	}()
	select {
	case publicURL := <-urlChannel:
		manager.mu.Lock()
		manager.status.PublicURL = publicURL
		manager.mu.Unlock()
		return publicURL, nil
	case err := <-errorChannel:
		if err == nil {
			err = errors.New("cloudflared exited before publishing a URL")
		}
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (manager *Manager) Stop() error {
	manager.mu.Lock()
	command := manager.cmd
	cancel := manager.cancel
	manager.cancel = nil
	manager.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if command == nil || command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (manager *Manager) superviseFixed(ctx context.Context, binary string, arguments []string, command *exec.Cmd) {
	for {
		err := command.Wait()
		manager.mu.Lock()
		manager.status.Running = false
		manager.status.Err = err
		manager.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}

		command = exec.CommandContext(ctx, binary, arguments...)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		output, pipeErr := command.StderrPipe()
		if pipeErr != nil {
			manager.setRestartError(pipeErr)
			continue
		}
		command.Stdout = command.Stderr
		if startErr := command.Start(); startErr != nil {
			manager.setRestartError(startErr)
			continue
		}
		manager.mu.Lock()
		manager.cmd = command
		manager.status.Running = true
		manager.status.RestartCount++
		manager.status.Err = nil
		manager.mu.Unlock()
		go manager.consume(output, make(chan string, 1))
	}
}

func (manager *Manager) setRestartError(err error) {
	manager.mu.Lock()
	manager.status.Running = false
	manager.status.Err = err
	manager.mu.Unlock()
}

func (manager *Manager) Status() Status {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.status
}

func (manager *Manager) consume(reader io.Reader, publicURL chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		if value := ParseQuickURL(scanner.Text()); value != "" {
			select {
			case publicURL <- value:
			default:
			}
		}
	}
}

func ParseQuickURL(line string) string {
	for _, field := range strings.Fields(line) {
		candidate := strings.Trim(field, "\"'(),[]{}")
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.Scheme == "https" && strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".trycloudflare.com") {
			return parsed.String()
		}
	}
	return ""
}

func commandArguments(mode, listen, tokenFile string) ([]string, error) {
	switch mode {
	case "quick":
		return []string{"tunnel", "--no-autoupdate", "--url", "http://" + listen}, nil
	case "fixed":
		info, err := os.Stat(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("open cloudflare token file: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("cloudflare token file permissions must be 0600 or stricter")
		}
		token, err := os.ReadFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read cloudflare token file: %w", err)
		}
		value := strings.TrimSpace(string(token))
		if value == "" {
			return nil, errors.New("cloudflare token file is empty")
		}
		return []string{"tunnel", "--no-autoupdate", "run", "--token", value}, nil
	default:
		return nil, errors.New("unsupported cloudflare mode")
	}
}
