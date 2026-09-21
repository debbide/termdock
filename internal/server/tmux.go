package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var tmuxSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var tmuxWindowIndexPattern = regexp.MustCompile(`^[0-9]+$`)

type tmuxSession struct {
	Name         string `json:"name"`
	Windows      int    `json:"windows"`
	Attached     bool   `json:"attached"`
	CreatedAt    string `json:"created_at"`
	LastActivity string `json:"last_activity"`
}

type tmuxWindow struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Panes  int    `json:"panes"`
}

type tmuxTarget struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	Name    string `json:"name,omitempty"`
}

type tmuxCommandRunner func(name string, args ...string) ([]byte, error)

func runTmuxCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func validTmuxSessionName(name string) bool {
	return tmuxSessionNamePattern.MatchString(name)
}

func tmuxInstallHelp() []string {
	return []string{
		"Debian/Ubuntu: sudo apt update && sudo apt install -y tmux",
		"Alpine: sudo apk add tmux",
		"Rocky/Fedora: sudo dnf install -y tmux",
	}
}

func parseTmuxSessions(output []byte) ([]tmuxSession, error) {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return []tmuxSession{}, nil
	}
	sessions := make([]tmuxSession, 0)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(fields) != 5 {
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
		activity, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid tmux activity time: %w", err)
		}
		sessions = append(sessions, tmuxSession{Name: fields[0], Windows: windows, Attached: fields[2] == "1", CreatedAt: time.Unix(created, 0).Format(time.RFC3339), LastActivity: time.Unix(activity, 0).Format(time.RFC3339)})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
	return sessions, nil
}

func parseTmuxWindows(output []byte) ([]tmuxWindow, error) {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return []tmuxWindow{}, nil
	}
	windows := make([]tmuxWindow, 0)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("unexpected tmux window output")
		}
		index, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("invalid tmux window index: %w", err)
		}
		panes, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, fmt.Errorf("invalid tmux pane count: %w", err)
		}
		windows = append(windows, tmuxWindow{Index: index, Name: fields[1], Active: fields[2] == "1", Panes: panes})
	}
	return windows, nil
}

func (server *Server) tmuxAvailable() bool {
	lookPath := server.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath("tmux")
	return err == nil
}

func noTmuxServer(output []byte) bool {
	return bytes.Contains(output, []byte("no server running")) || bytes.Contains(output, []byte("failed to connect to server"))
}

func (server *Server) tmuxDefault() string {
	server.tmuxMu.Lock()
	defer server.tmuxMu.Unlock()
	return server.defaultTmux
}

func (server *Server) setTmuxDefault(value string) {
	server.tmuxMu.Lock()
	server.defaultTmux = value
	server.tmuxMu.Unlock()
}

func (server *Server) currentTmuxTarget() *tmuxTarget {
	server.sessionMu.Lock()
	terminalSession := server.session
	server.sessionMu.Unlock()
	if terminalSession == nil {
		return nil
	}
	return terminalSession.currentTmuxTarget()
}

func (server *Server) writeTmuxUnavailable(writer http.ResponseWriter) {
	writeJSON(writer, map[string]any{"available": false, "sessions": []tmuxSession{}, "install_help": tmuxInstallHelp()})
}

func (server *Server) tmuxStateHandler(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !server.tmuxAvailable() {
		server.writeTmuxUnavailable(writer)
		return
	}
	writeJSON(writer, map[string]any{"available": true, "current_target": server.currentTmuxTarget(), "default_target": server.tmuxDefault(), "copy_mode_hint": "复制模式：Ctrl+b 后按 [，方向键滚动，按 q 退出"})
}

func (server *Server) listTmuxSessions(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !server.tmuxAvailable() {
		server.writeTmuxUnavailable(writer)
		return
	}
	output, err := server.runCommand("tmux", "list-sessions", "-F", "#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}\t#{session_activity}")
	if err != nil {
		if noTmuxServer(output) {
			writeJSON(writer, map[string]any{"available": true, "sessions": []tmuxSession{}, "current_target": nil, "default_target": server.tmuxDefault()})
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
	writeJSON(writer, map[string]any{"available": true, "sessions": sessions, "current_target": server.currentTmuxTarget(), "default_target": server.tmuxDefault(), "copy_mode_hint": "复制模式：Ctrl+b 后按 [，方向键滚动，按 q 退出"})
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
		http.Error(writer, strings.Join(tmuxInstallHelp(), "\n"), http.StatusServiceUnavailable)
		return
	}
	if output, err := server.runCommand("tmux", "new-session", "-d", "-s", input.Name, "-c", server.cfg.Terminal.WorkingDir); err != nil {
		if strings.Contains(string(output), "duplicate session") {
			http.Error(writer, "同名 tmux 会话已存在", http.StatusConflict)
			return
		}
		http.Error(writer, "创建 tmux 会话失败", http.StatusInternalServerError)
		return
	}
	if server.tmuxDefault() == "" {
		server.setTmuxDefault(input.Name)
	}
	writer.WriteHeader(http.StatusCreated)
}

func (server *Server) listTmuxWindows(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := request.PathValue("name")
	if !validTmuxSessionName(name) {
		http.Error(writer, "tmux 会话名称无效", http.StatusBadRequest)
		return
	}
	output, err := server.runCommand("tmux", "list-windows", "-t", name, "-F", "#{window_index}\t#{window_name}\t#{window_active}\t#{window_panes}")
	if err != nil {
		http.Error(writer, "tmux 会话不存在", http.StatusNotFound)
		return
	}
	windows, err := parseTmuxWindows(output)
	if err != nil {
		http.Error(writer, "无法解析 tmux 窗口", http.StatusInternalServerError)
		return
	}
	writeJSON(writer, map[string]any{"session": name, "windows": windows})
}

func (server *Server) switchTmuxTarget(name string, window *int) error {
	if _, err := server.runCommand("tmux", "has-session", "-t", name); err != nil {
		return err
	}
	_, _ = server.runCommand("tmux", "set-option", "-t", name, "mouse", "on")

	server.sessionMu.Lock()
	terminalSession := server.session
	server.sessionMu.Unlock()
	if terminalSession == nil {
		return fmt.Errorf("terminal unavailable")
	}

	target := name
	selectedWindow := 0
	if window != nil {
		selectedWindow = *window
		target = fmt.Sprintf("%s:%d", name, selectedWindow)
	} else if current := terminalSession.currentTmuxTarget(); current != nil && current.Session == name {
		selectedWindow = current.Window
	}

	current := terminalSession.currentTmuxTarget()
	if current == nil {
		if _, err := terminalSession.terminal.Write([]byte("tmux attach-session -t " + target + "\n")); err != nil {
			return err
		}
		terminalSession.setTmuxTarget(tmuxTarget{Session: name, Window: selectedWindow})
		return nil
	}

	if current.Session != name {
		if _, err := server.runCommand("tmux", "switch-client", "-t", name); err != nil {
			return err
		}
	}
	if window != nil {
		if _, err := server.runCommand("tmux", "select-window", "-t", target); err != nil {
			return err
		}
	}
	terminalSession.setTmuxTarget(tmuxTarget{Session: name, Window: selectedWindow})
	return nil
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
		http.Error(writer, strings.Join(tmuxInstallHelp(), "\n"), http.StatusServiceUnavailable)
		return
	}
	if err := server.switchTmuxTarget(name, nil); err != nil {
		if strings.Contains(err.Error(), "terminal unavailable") {
			http.Error(writer, "终端尚未连接", http.StatusConflict)
		} else {
			http.Error(writer, "tmux 会话不存在或无法切换", http.StatusNotFound)
		}
		return
	}
	writeJSON(writer, map[string]any{"current_target": server.currentTmuxTarget()})
}

func (server *Server) detachTmuxSession(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	server.sessionMu.Lock()
	terminalSession := server.session
	server.sessionMu.Unlock()
	if terminalSession == nil {
		http.Error(writer, "终端尚未连接", http.StatusConflict)
		return
	}
	current := terminalSession.currentTmuxTarget()
	if current == nil {
		http.Error(writer, "当前未连接 tmux 会话", http.StatusConflict)
		return
	}
	if _, err := terminalSession.terminal.Write([]byte("\x02d")); err != nil {
		http.Error(writer, "退出 tmux 会话失败", http.StatusInternalServerError)
		return
	}
	terminalSession.clearTmuxTarget(current.Session)
	writeJSON(writer, map[string]any{"current_target": nil})
}

func (server *Server) selectTmuxWindow(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, raw := request.PathValue("name"), request.PathValue("index")
	if !validTmuxSessionName(name) || !tmuxWindowIndexPattern.MatchString(raw) {
		http.Error(writer, "tmux 目标无效", http.StatusBadRequest)
		return
	}
	index, _ := strconv.Atoi(raw)
	if err := server.switchTmuxTarget(name, &index); err != nil {
		http.Error(writer, "tmux 窗口不存在或无法切换", http.StatusNotFound)
		return
	}
	writeJSON(writer, map[string]any{"current_target": server.currentTmuxTarget()})
}

func (server *Server) setDefaultTmuxTarget(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var input struct {
		Session string `json:"session"`
	}
	if json.NewDecoder(request.Body).Decode(&input) != nil || (input.Session != "" && !validTmuxSessionName(input.Session)) {
		http.Error(writer, "默认 tmux 会话无效", http.StatusBadRequest)
		return
	}
	if input.Session != "" {
		if _, err := server.runCommand("tmux", "has-session", "-t", input.Session); err != nil {
			http.Error(writer, "tmux 会话不存在", http.StatusNotFound)
			return
		}
	}
	server.setTmuxDefault(input.Session)
	writeJSON(writer, map[string]any{"default_target": input.Session})
}

func (server *Server) renameTmuxSession(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	oldName := request.PathValue("name")
	if !validTmuxSessionName(oldName) {
		http.Error(writer, "tmux 会话名称无效", http.StatusBadRequest)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Name string `json:"name"`
	}
	if err := decoder.Decode(&input); err != nil {
		http.Error(writer, "请求 JSON 无效", http.StatusBadRequest)
		return
	}
	if !validTmuxSessionName(input.Name) {
		http.Error(writer, "会话名称仅支持字母、数字、点、下划线和连字符，最长 64 个字符", http.StatusBadRequest)
		return
	}
	if input.Name == oldName {
		writeJSON(writer, map[string]any{"old_name": oldName, "name": input.Name, "current_target": server.currentTmuxTarget(), "default_target": server.tmuxDefault()})
		return
	}
	if !server.tmuxAvailable() {
		http.Error(writer, strings.Join(tmuxInstallHelp(), "\n"), http.StatusServiceUnavailable)
		return
	}

	server.tmuxMu.Lock()
	defer server.tmuxMu.Unlock()
	if _, err := server.runCommand("tmux", "has-session", "-t", oldName); err != nil {
		http.Error(writer, "tmux 会话不存在", http.StatusNotFound)
		return
	}
	if _, err := server.runCommand("tmux", "has-session", "-t", input.Name); err == nil {
		http.Error(writer, "同名 tmux 会话已存在", http.StatusConflict)
		return
	}
	if output, err := server.runCommand("tmux", "rename-session", "-t", oldName, input.Name); err != nil {
		if strings.Contains(string(output), "duplicate session") {
			http.Error(writer, "同名 tmux 会话已存在", http.StatusConflict)
			return
		}
		http.Error(writer, "重命名 tmux 会话失败", http.StatusInternalServerError)
		return
	}
	if server.defaultTmux == oldName {
		server.defaultTmux = input.Name
	}
	server.sessionMu.Lock()
	terminalSession := server.session
	server.sessionMu.Unlock()
	if terminalSession != nil {
		terminalSession.renameTmuxTarget(oldName, input.Name)
	}
	writeJSON(writer, map[string]any{"old_name": oldName, "name": input.Name, "current_target": server.currentTmuxTarget(), "default_target": server.defaultTmux})
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
		http.Error(writer, strings.Join(tmuxInstallHelp(), "\n"), http.StatusServiceUnavailable)
		return
	}
	if current := server.currentTmuxTarget(); current != nil && current.Session == name {
		server.sessionMu.Lock()
		terminalSession := server.session
		server.sessionMu.Unlock()
		if terminalSession != nil {
			_, _ = terminalSession.terminal.Write([]byte("\x02d"))
			terminalSession.clearTmuxTarget(name)
		}
	}
	if _, err := server.runCommand("tmux", "kill-session", "-t", name); err != nil {
		http.Error(writer, "tmux 会话不存在或无法删除", http.StatusNotFound)
		return
	}
	if server.tmuxDefault() == name {
		server.setTmuxDefault("")
	}
	writer.WriteHeader(http.StatusNoContent)
}
