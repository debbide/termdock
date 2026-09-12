package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"webterm-cf/internal/auth"
	"webterm-cf/internal/config"
	"webterm-cf/internal/session"
	"webterm-cf/internal/terminal"
)

const cookieName = "webterm_session"

type Server struct {
	cfg       config.Config
	auth      *auth.Manager
	limiter   *session.Limiter
	assets    fs.FS
	started   time.Time
	sessionMu sync.Mutex
	session   *persistentSession
}

func New(cfg config.Config, manager *auth.Manager, assets fs.FS) *Server {
	return &Server{cfg: cfg, auth: manager, limiter: session.NewLimiter(cfg.Terminal.MaxSessions), assets: assets, started: time.Now()}
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/status", server.status)
	mux.HandleFunc("POST /api/auth/token", server.exchangeToken)
	mux.HandleFunc("POST /api/auth/logout", server.logout)
	mux.HandleFunc("POST /api/session/terminate", server.terminate)
	mux.HandleFunc("GET /api/terminal/ws", server.webSocket)
	mux.HandleFunc("GET /api/files", server.listFiles)
	mux.HandleFunc("GET /api/files/download", server.downloadFile)
	mux.HandleFunc("GET /api/files/content", server.readFile)
	mux.HandleFunc("PUT /api/files/content", server.writeFile)
	mux.HandleFunc("DELETE /api/files", server.deleteFile)
	mux.HandleFunc("POST /api/files/upload", server.uploadFile)
	mux.HandleFunc("POST /api/files/directory", server.createDirectory)
	mux.HandleFunc("POST /api/files/archive", server.archiveFile)
	mux.HandleFunc("POST /api/files/operations", server.fileOperation)
	mux.Handle("/", http.FileServer(http.FS(server.assets)))
	return securityHeaders(mux)
}

type fileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modified"`
	IsDir   bool   `json:"is_dir"`
}

func (server *Server) filePath(requested string) (string, error) {
	if requested == "" {
		requested = server.cfg.Terminal.WorkingDir
	}
	cleaned := filepath.Clean(requested)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(server.cfg.Terminal.WorkingDir, cleaned)
	}
	return cleaned, nil
}

func (server *Server) listFiles(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	path, err := server.filePath(request.URL.Query().Get("path"))
	if err != nil {
		http.Error(writer, "invalid path", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	result := make([]fileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, fileEntry{Name: entry.Name(), Path: filepath.Join(path, entry.Name()), Size: info.Size(), Mode: info.Mode().String(), ModTime: info.ModTime().Format(time.RFC3339), IsDir: entry.IsDir()})
	}
	writeJSON(writer, map[string]any{"path": path, "parent": filepath.Dir(path), "entries": result})
}

func (server *Server) downloadFile(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	path, err := server.filePath(request.URL.Query().Get("path"))
	if err != nil {
		http.Error(writer, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.Error(writer, "file not found", http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	http.ServeFile(writer, request, path)
}

func (server *Server) readFile(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "未登录", http.StatusUnauthorized)
		return
	}
	path, err := server.filePath(request.URL.Query().Get("path"))
	if err != nil {
		http.Error(writer, "路径无效", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.Error(writer, "文件不存在", http.StatusNotFound)
		return
	}
	if info.Size() > 2<<20 {
		http.Error(writer, "文件超过 2 MB，无法在线编辑", http.StatusRequestEntityTooLarge)
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		http.Error(writer, "读取文件失败", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = writer.Write(content)
}

func (server *Server) writeFile(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "未登录", http.StatusUnauthorized)
		return
	}
	path, err := server.filePath(request.URL.Query().Get("path"))
	if err != nil {
		http.Error(writer, "路径无效", http.StatusBadRequest)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 2<<20)
	content, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "文件超过 2 MB，无法保存", http.StatusRequestEntityTooLarge)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.Error(writer, "文件不存在", http.StatusNotFound)
		return
	}
	if err := os.WriteFile(path, content, info.Mode().Perm()); err != nil {
		http.Error(writer, "保存文件失败", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) deleteFile(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "未登录", http.StatusUnauthorized)
		return
	}
	path, err := server.filePath(request.URL.Query().Get("path"))
	if err != nil {
		http.Error(writer, "路径无效", http.StatusBadRequest)
		return
	}
	if path == "/" || filepath.Clean(path) == filepath.Clean(server.cfg.Terminal.WorkingDir) {
		http.Error(writer, "不能删除根目录", http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(path); err != nil {
		http.Error(writer, "删除失败", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) uploadFile(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 512<<20)
	if err := request.ParseMultipartForm(32 << 20); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			slog.Warn("upload rejected", "reason", "request too large", "limit", maxBytesError.Limit)
			http.Error(writer, "文件超过 512 MiB 上传限制", http.StatusRequestEntityTooLarge)
			return
		}
		slog.Warn("upload parsing failed", "error", err)
		http.Error(writer, "上传数据不完整或格式无效，请检查网络后重试", http.StatusBadRequest)
		return
	}
	directory, err := server.filePath(request.FormValue("path"))
	if err != nil {
		http.Error(writer, "invalid path", http.StatusBadRequest)
		return
	}
	source, header, err := request.FormFile("file")
	if err != nil {
		http.Error(writer, "file is required", http.StatusBadRequest)
		return
	}
	defer source.Close()
	name := filepath.Base(header.Filename)
	if name == "." || name == string(filepath.Separator) {
		http.Error(writer, "invalid filename", http.StatusBadRequest)
		return
	}
	finalPath := filepath.Join(directory, name)
	if _, err := os.Stat(finalPath); err == nil {
		http.Error(writer, "file already exists", http.StatusConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	destination, err := os.CreateTemp(directory, ".termdock-upload-*")
	if err != nil {
		http.Error(writer, "cannot create upload file", http.StatusInternalServerError)
		return
	}
	temporaryPath := destination.Name()
	completed := false
	defer func() {
		destination.Close()
		if !completed {
			os.Remove(temporaryPath)
		}
	}()
	if err := destination.Chmod(0o600); err != nil {
		http.Error(writer, "cannot prepare upload file", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(destination, source); err != nil {
		http.Error(writer, "upload failed", http.StatusInternalServerError)
		return
	}
	if err := destination.Sync(); err != nil {
		http.Error(writer, "upload failed", http.StatusInternalServerError)
		return
	}
	if err := destination.Close(); err != nil {
		http.Error(writer, "upload failed", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		http.Error(writer, "cannot finish upload", http.StatusInternalServerError)
		return
	}
	completed = true
	writer.WriteHeader(http.StatusCreated)
}

func (server *Server) createDirectory(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var input struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if json.NewDecoder(request.Body).Decode(&input) != nil || filepath.Base(input.Name) != input.Name || input.Name == "." {
		http.Error(writer, "invalid directory", http.StatusBadRequest)
		return
	}
	directory, _ := server.filePath(input.Path)
	if err := os.Mkdir(filepath.Join(directory, input.Name), 0o700); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusCreated)
}

func (server *Server) status(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(writer, map[string]any{"active_sessions": server.limiter.Active(), "uptime_seconds": int(time.Since(server.started).Seconds())})
}

func (server *Server) exchangeToken(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var input struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(request.Body).Decode(&input) != nil {
		http.Error(writer, "invalid credentials", http.StatusUnauthorized)
		return
	}
	cookie, err := server.auth.Exchange(input.Token)
	if err != nil {
		time.Sleep(250 * time.Millisecond)
		http.Error(writer, "invalid credentials", http.StatusUnauthorized)
		return
	}
	http.SetCookie(writer, &http.Cookie{Name: cookieName, Value: cookie, Path: "/", HttpOnly: true, Secure: server.cookieSecure(request), SameSite: http.SameSiteStrictMode, MaxAge: int(server.cfg.Terminal.MaxLifetime.Seconds())})
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) logout(writer http.ResponseWriter, request *http.Request) {
	http.SetCookie(writer, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: server.cookieSecure(request), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) cookieSecure(request *http.Request) bool {
	if !server.cfg.Security.CookieSecure {
		return false
	}
	return request.TLS != nil || strings.EqualFold(request.Header.Get("X-Forwarded-Proto"), "https")
}

func (server *Server) terminate(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	server.closeTerminalSession()
	server.logout(writer, request)
}

func (server *Server) webSocket(writer http.ResponseWriter, request *http.Request) {
	if !server.authenticated(request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !server.validOrigin(request) {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connection, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(server.cfg.Security.MaxMessageSize)

	var terminalSession *persistentSession
	var clientID uint64
	for attempt := 0; attempt < 2; attempt++ {
		terminalSession, err = server.terminalSession()
		if err != nil {
			break
		}
		clientID, err = terminalSession.attach(connection)
		if !errors.Is(err, errTerminalSessionClosed) {
			break
		}
	}
	if err != nil {
		slog.Error("terminal attach failed", "shell", server.cfg.Terminal.Shell, "working_directory", server.cfg.Terminal.WorkingDir, "error", err)
		_ = connection.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "terminal unavailable"))
		return
	}
	defer terminalSession.detach(connection, clientID)
	server.bridge(request.Context(), connection, terminalSession, clientID)
}

func (server *Server) terminalSession() (*persistentSession, error) {
	server.sessionMu.Lock()
	defer server.sessionMu.Unlock()
	if server.session != nil {
		select {
		case <-server.session.closed:
			// The PTY has already exited. Release its slot here before creating
			// the replacement session. The cleanup goroutine checks the session
			// identity, so it will not release the same slot a second time.
			server.session = nil
			server.limiter.Release()
		default:
			return server.session, nil
		}
	}
	if !server.limiter.Acquire() {
		return nil, errors.New("session limit reached")
	}
	ptySession, err := terminal.Start(server.cfg.Terminal.Shell, server.cfg.Terminal.WorkingDir)
	if err != nil {
		server.limiter.Release()
		return nil, err
	}
	server.session = newPersistentSession(ptySession, server.cfg.Terminal.SessionRetention)
	current := server.session
	go func() {
		if server.cfg.Terminal.MaxLifetime > 0 {
			timer := time.NewTimer(server.cfg.Terminal.MaxLifetime)
			defer timer.Stop()
			select {
			case <-current.done:
			case <-timer.C:
				current.close()
			}
		} else {
			<-current.done
		}
		server.sessionMu.Lock()
		if server.session == current {
			server.session = nil
			server.limiter.Release()
		}
		server.sessionMu.Unlock()
	}()
	return current, nil
}

func (server *Server) closeTerminalSession() {
	server.sessionMu.Lock()
	current := server.session
	server.sessionMu.Unlock()
	if current != nil {
		current.close()
	}
}

func (server *Server) bridge(parent context.Context, connection *websocket.Conn, terminalSession *persistentSession, clientID uint64) {
	activity := make(chan struct{}, 1)
	errorsChannel := make(chan error, 1)
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	go func() {
		for {
			messageType, data, err := connection.ReadMessage()
			if err != nil {
				select {
				case errorsChannel <- err:
				case <-parent.Done():
				}
				return
			}
			select {
			case activity <- struct{}{}:
			default:
			}
			switch messageType {
			case websocket.BinaryMessage:
				err = terminalSession.write(connection, clientID, data)
			case websocket.TextMessage:
				var control struct {
					Type string `json:"type"`
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if json.Unmarshal(data, &control) != nil {
					continue
				}
				if control.Type == "resize" {
					err = terminalSession.resize(control.Cols, control.Rows)
				}
			}
			if err != nil {
				select {
				case errorsChannel <- err:
				case <-parent.Done():
				}
				return
			}
		}
	}()

	var idleTimer *time.Timer
	var idle <-chan time.Time
	if server.cfg.Terminal.IdleTimeout > 0 {
		idleTimer = time.NewTimer(server.cfg.Terminal.IdleTimeout)
		idle = idleTimer.C
		defer idleTimer.Stop()
	}
	for {
		select {
		case <-parent.Done():
			return
		case err := <-errorsChannel:
			slog.Info("terminal connection detached", "reason", "bridge error", "error", err)
			return
		case <-heartbeat.C:
			if err := terminalSession.ping(connection, clientID); err != nil {
				slog.Info("terminal connection detached", "reason", "heartbeat error", "error", err)
				return
			}
		case <-activity:
			if idleTimer != nil {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(server.cfg.Terminal.IdleTimeout)
			}
		case <-idle:
			slog.Info("terminal connection detached", "reason", "idle timeout")
			return
		}
	}
}

func (server *Server) authenticated(request *http.Request) bool {
	cookie, err := request.Cookie(cookieName)
	return err == nil && server.auth.Validate(cookie.Value)
}
func (server *Server) validOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	requestHost := request.Host
	if forwardedHost := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Host"), ",")[0]); forwardedHost != "" {
		requestHost = forwardedHost
	}
	if strings.EqualFold(parsed.Host, requestHost) {
		return true
	}
	for _, trusted := range server.cfg.Security.TrustedOrigins {
		if strings.EqualFold(strings.TrimSuffix(origin, "/"), strings.TrimSuffix(trusted, "/")) {
			return true
		}
	}
	return false
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(writer, request)
	})
}
func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}
