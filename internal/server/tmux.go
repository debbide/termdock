package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var tmuxSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type tmuxSession struct {
	Name      string `json:"name"`
	Windows   int    `json:"windows"`
	Attached  bool   `json:"attached"`
	CreatedAt string `json:"created_at"`
}

type tmuxCommandRunner func(name string, args ...string) ([]byte, error)

func runTmuxCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func validTmuxSessionName(name string) bool {
	return tmuxSessionNamePattern.MatchString(name)
}

func parseTmuxSessions(output []byte) ([]tmuxSession, error) {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return []tmuxSession{}, nil
	}
	sessions := make([]tmuxSession, 0)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("unexpected tmux output")
		}
		windows, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid tmux window count: %w", err)
		}
		created, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid tmux creation time: %w", err)
		}
		sessions = append(sessions, tmuxSession{
			Name:      fields[0],
			Windows:   windows,
			Attached:  fields[2] == "1",
			CreatedAt: time.Unix(created, 0).Format(time.RFC3339),
		})
	}
	return sessions, nil
}

func (server *Server) tmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

func (server *Server) listTmuxSessions(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !server.tmuxAvailable() {
		writeJSON(writer, map[string]any{"available": false, "sessions": []tmuxSession{}})
		return
	}
	output, err := server.runCommand("tmux", "list-sessions", "-F", "#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}")
	if err != nil {
		if bytes.Contains(output, []byte("no server running")) || bytes.Contains(output, []byte("failed to connect to server")) {
			writeJSON(writer, map[string]any{"available": true, "sessions": []tmuxSession{}})
			return
		}
		http.Error(writer, "无法读取 tmux 会话", http.StatusInternalServerError)
		return
	}
	sessions, err := parseTmuxSessions(output)
	if err != nil {
		http.Error(writer, "无法解析 tmux 会话", http.StatusInternalServerError)
		return
	}
	writeJSON(writer, map[string]any{"available": true, "sessions": sessions})
}

func (server *Server) createTmuxSession(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var input struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(request.Body).Decode(&input) != nil || !validTmuxSessionName(input.Name) {
		http.Error(writer, "会话名称仅支持字母、数字、点、下划线和连字符，最长 64 个字符", http.StatusBadRequest)
		return
	}
	if !server.tmuxAvailable() {
		http.Error(writer, "服务器未安装 tmux", http.StatusServiceUnavailable)
		return
	}
	if output, err := server.runCommand("tmux", "new-session", "-d", "-s", input.Name, "-c", server.cfg.Terminal.WorkingDir); err != nil {
		message := strings.TrimSpace(string(output))
		if strings.Contains(message, "duplicate session") {
			http.Error(writer, "同名 tmux 会话已存在", http.StatusConflict)
			return
		}
		http.Error(writer, "创建 tmux 会话失败", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusCreated)
}

func (server *Server) attachTmuxSession(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := request.PathValue("name")
	if !validTmuxSessionName(name) {
		http.Error(writer, "tmux 会话名称无效", http.StatusBadRequest)
		return
	}
	if !server.tmuxAvailable() {
		http.Error(writer, "服务器未安装 tmux", http.StatusServiceUnavailable)
		return
	}
	if _, err := server.runCommand("tmux", "has-session", "-t", name); err != nil {
		http.Error(writer, "tmux 会话不存在", http.StatusNotFound)
		return
	}
	server.sessionMu.Lock()
	current := server.session
	server.sessionMu.Unlock()
	if current == nil {
		http.Error(writer, "终端尚未连接", http.StatusConflict)
		return
	}
	command := "tmux attach-session -t " + name + "\n"
	if _, err := current.terminal.Write([]byte(command)); err != nil {
		http.Error(writer, "无法连接 tmux 会话", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) killTmuxSession(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := request.PathValue("name")
	if !validTmuxSessionName(name) {
		http.Error(writer, "tmux 会话名称无效", http.StatusBadRequest)
		return
	}
	if !server.tmuxAvailable() {
		http.Error(writer, "服务器未安装 tmux", http.StatusServiceUnavailable)
		return
	}
	if _, err := server.runCommand("tmux", "kill-session", "-t", name); err != nil {
		http.Error(writer, "tmux 会话不存在或无法结束", http.StatusNotFound)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
