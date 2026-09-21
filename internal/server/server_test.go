package server

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"webterm-cf/internal/auth"
	"webterm-cf/internal/config"
)

func newTestServer(t *testing.T, configure ...func(*config.Config)) (*Server, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Security.CookieSecure = false
	for _, apply := range configure {
		apply(&cfg)
	}
	manager, token, err := auth.NewWithToken(time.Minute, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, manager, assets), token
}

func TestStatusRequiresAuthentication(t *testing.T) {
	server, _ := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status code = %d", response.Code)
	}
}

func TestWebSocketRejectsMissingOrInvalidOrigin(t *testing.T) {
	server, token := newTestServer(t)
	cookie, err := server.auth.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"", "https://attacker.invalid"} {
		request := httptest.NewRequest(http.MethodGet, "/api/terminal/ws", nil)
		request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("origin %q returned %d", origin, response.Code)
		}
	}
}

func TestValidOriginAcceptsForwardedHost(t *testing.T) {
	server, _ := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/api/terminal/ws", nil)
	request.Host = "127.0.0.1:7681"
	request.RemoteAddr = "127.0.0.1:5000"
	request.Header.Set("Origin", "https://mc.bbe.pp.ua")
	request.Header.Set("X-Forwarded-Host", "mc.bbe.pp.ua")

	if !server.validOrigin(request) {
		t.Fatal("expected forwarded host to satisfy origin validation")
	}
}

func TestValidOriginIgnoresForwardedHostFromUntrustedPeer(t *testing.T) {
	server, _ := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/api/terminal/ws", nil)
	request.Host = "127.0.0.1:7681"
	request.RemoteAddr = "203.0.113.9:5000"
	request.Header.Set("Origin", "https://attacker.invalid")
	request.Header.Set("X-Forwarded-Host", "attacker.invalid")

	if server.validOrigin(request) {
		t.Fatal("a remote peer spoofed X-Forwarded-Host past origin validation")
	}
}

func TestForwardedHeadersRequireTrustedPeer(t *testing.T) {
	server, _ := newTestServer(t)

	loopback := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/", nil)
	loopback.RemoteAddr = "127.0.0.1:5000"
	if !server.forwardedTrusted(loopback) {
		t.Fatal("loopback peer must be trusted")
	}

	remote := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/", nil)
	remote.RemoteAddr = "203.0.113.9:5000"
	if server.forwardedTrusted(remote) {
		t.Fatal("remote peer must not be trusted by default")
	}

	// An explicitly configured proxy range is trusted, but only inside it.
	server.cfg.Security.TrustedProxies = []string{"10.0.0.0/8", "198.51.100.7"}
	inside := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/", nil)
	inside.RemoteAddr = "10.4.5.6:5000"
	if !server.forwardedTrusted(inside) {
		t.Fatal("configured proxy range was not trusted")
	}
	exact := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/", nil)
	exact.RemoteAddr = "198.51.100.7:5000"
	if !server.forwardedTrusted(exact) {
		t.Fatal("configured proxy address was not trusted")
	}
	if server.forwardedTrusted(remote) {
		t.Fatal("unlisted address was trusted")
	}
}

func TestForwardedProtoOnlySecuresCookieFromTrustedPeer(t *testing.T) {
	server, _ := newTestServer(t)
	server.cfg.Security.CookieSecure = true

	trusted := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/", nil)
	trusted.RemoteAddr = "127.0.0.1:5000"
	trusted.Header.Set("X-Forwarded-Proto", "https")
	if !server.cookieSecure(trusted) {
		t.Fatal("trusted peer with X-Forwarded-Proto https must produce a secure cookie")
	}

	untrusted := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7681/", nil)
	untrusted.RemoteAddr = "203.0.113.9:5000"
	untrusted.Header.Set("X-Forwarded-Proto", "https")
	if server.cookieSecure(untrusted) {
		t.Fatal("untrusted peer spoofed X-Forwarded-Proto into a secure cookie")
	}
}

func TestHealthDoesNotLeakDetails(t *testing.T) {
	server, _ := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("unexpected health response: %d %q", response.Code, response.Body.String())
	}
}

func TestAuthenticationCookieMatchesTransport(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		forwardedProto string
		secure         bool
	}{
		{name: "local http", secure: false},
		{name: "forwarded https", forwardedProto: "https", secure: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server, token := newTestServer(t)
			server.cfg.Security.CookieSecure = true
			request := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"`+token+`"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-Proto", testCase.forwardedProto)
			request.RemoteAddr = "127.0.0.1:5000"
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status code = %d", response.Code)
			}
			cookies := response.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Secure != testCase.secure {
				t.Fatalf("unexpected cookies: %#v", cookies)
			}
		})
	}
}

func TestClosedPersistentSessionReturnsSentinelError(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	session := &persistentSession{closed: closed}

	_, err := session.attach(nil)
	if !errors.Is(err, errTerminalSessionClosed) {
		t.Fatalf("attach error = %v, want %v", err, errTerminalSessionClosed)
	}
}

// fakeTerminal stands in for a real PTY so session lifecycle can be exercised
// without spawning a shell.
type fakeTerminal struct {
	mu     sync.Mutex
	closed bool
}

func (fake *fakeTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (fake *fakeTerminal) Write(data []byte) (int, error) {
	return len(data), nil
}
func (fake *fakeTerminal) Resize(_, _ uint16) error { return nil }
func (fake *fakeTerminal) Close() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.closed = true
	return nil
}
func (fake *fakeTerminal) isClosed() bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.closed
}

// blockingTerminal never finishes closing, standing in for a PTY whose shell
// refuses to exit.
type blockingTerminal struct {
	release chan struct{}
}

func (blocked *blockingTerminal) Read([]byte) (int, error)       { return 0, io.EOF }
func (blocked *blockingTerminal) Write(data []byte) (int, error) { return len(data), nil }
func (blocked *blockingTerminal) Resize(_, _ uint16) error       { return nil }
func (blocked *blockingTerminal) Close() error                   { <-blocked.release; return nil }

func TestLogoutRevokesCookieAndEndsTerminalSession(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.CookieSecure = false
	manager, token, err := auth.NewWithToken(time.Minute, "reusable-test-token")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := fs.Sub(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	server := New(cfg, manager, assets)
	terminalProcess := &fakeTerminal{}
	server.session = &persistentSession{terminal: terminalProcess, closed: make(chan struct{})}

	login := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"`+token+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}

	first := login()
	if first.Code != http.StatusNoContent {
		t.Fatalf("first login status = %d", first.Code)
	}
	cookies := first.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("first login cookies = %#v", cookies)
	}
	if !manager.Validate(cookies[0].Value) {
		t.Fatal("issued cookie was not accepted")
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", logoutResponse.Code)
	}
	if manager.Validate(cookies[0].Value) {
		t.Fatal("logged-out cookie is still valid")
	}
	replay := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	replay.AddCookie(cookies[0])
	replayResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replayed cookie status = %d, want 401", replayResponse.Code)
	}
	if !terminalProcess.isClosed() {
		t.Fatal("logout did not close the PTY")
	}
	server.sessionMu.Lock()
	cleared := server.session == nil
	server.sessionMu.Unlock()
	if !cleared {
		t.Fatal("logout did not release the terminal session")
	}

	second := login()
	if second.Code != http.StatusNoContent {
		t.Fatalf("relogin status = %d", second.Code)
	}
}

// liveTerminal stays running until it is closed, like a shell sitting at a
// prompt. fakeTerminal's immediate io.EOF would end the session on its own and
// hide a leaked session slot.
type liveTerminal struct {
	closed chan struct{}
	once   sync.Once
}

func newLiveTerminal() *liveTerminal { return &liveTerminal{closed: make(chan struct{})} }

func (live *liveTerminal) Read([]byte) (int, error) {
	<-live.closed
	return 0, io.EOF
}
func (live *liveTerminal) Write(data []byte) (int, error) { return len(data), nil }
func (live *liveTerminal) Resize(_, _ uint16) error       { return nil }
func (live *liveTerminal) Close() error {
	live.once.Do(func() { close(live.closed) })
	return nil
}

// Logging out ends the session, so it must return the session's concurrency
// slot. When the slot leaks, the next login's terminal attach is refused with
// "session limit reached" and the terminal looks unreachable.
func TestLogoutReleasesSessionSlotForReconnect(t *testing.T) {
	server, token := newTestServer(t, func(cfg *config.Config) { cfg.Terminal.MaxSessions = 1 })
	server.startTerminal = func(string, string) (terminalProcess, error) { return newLiveTerminal(), nil }

	if _, err := server.terminalSession(); err != nil {
		t.Fatalf("first terminalSession: %v", err)
	}
	if active := server.limiter.Active(); active != 1 {
		t.Fatalf("active sessions = %d, want 1", active)
	}

	cookie, err := server.auth.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", response.Code)
	}

	if active := server.limiter.Active(); active != 0 {
		t.Fatalf("active sessions after logout = %d, want 0 (slot leaked)", active)
	}
	if _, err := server.terminalSession(); err != nil {
		t.Fatalf("terminalSession after logout: %v", err)
	}
}

// A PTY that never finishes tearing down must not be able to hang logout.
func TestLogoutReturnsEvenWhenPTYTeardownStalls(t *testing.T) {
	server, token := newTestServer(t)
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	server.session = &persistentSession{terminal: &blockingTerminal{release: blocked}, closed: make(chan struct{})}

	cookie, err := server.auth.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	response := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("logout hung on a stalled PTY teardown")
	}
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", response.Code)
	}
}

func TestUnauthenticatedLogoutCannotEndTerminalSession(t *testing.T) {
	server, _ := newTestServer(t)
	terminalProcess := &fakeTerminal{}
	terminalSession := &persistentSession{terminal: terminalProcess, closed: make(chan struct{})}
	server.session = terminalSession

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", response.Code)
	}
	if terminalProcess.isClosed() {
		t.Fatal("unauthenticated logout closed the PTY")
	}
	select {
	case <-terminalSession.closed:
		t.Fatal("unauthenticated logout ended the terminal session")
	default:
	}
}

// Failed logins must stay answerable: a rate limit keyed on the tunnel's
// loopback address would let any remote caller lock out the real operator.
func TestRepeatedFailedLoginsAreNotLockedOut(t *testing.T) {
	server, token := newTestServer(t)

	failed := func(address string) int {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"wrong"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = address
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response.Code
	}

	// More attempts than any former rate limit allowed.
	for attempt := 0; attempt < 8; attempt++ {
		if code := failed("203.0.113.9:5555"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt+1, code)
		}
	}

	// The real operator can still authenticate from the tunnel address.
	request := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"`+token+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:5555"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("successful login status = %d, want 204", response.Code)
	}
}

func TestClientAddressIgnoresForwardingHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/auth/token", nil)
	request.RemoteAddr = "127.0.0.1:5000"
	request.Header.Set("X-Forwarded-For", "203.0.113.7")
	request.Header.Set("X-Real-IP", "203.0.113.8")

	if address := clientAddress(request); address != "127.0.0.1" {
		t.Fatalf("client address = %q, want the direct peer", address)
	}
}

// The file API intentionally reaches the whole filesystem, matching the shell
// the operator already has. Only the working directory itself is protected from
// deletion so the configured root cannot be removed out from under the service.
func TestFileAPIReachesOutsideWorkingDirectory(t *testing.T) {
	server, token := newTestServer(t)
	workingDir := t.TempDir()
	server.cfg.Terminal.WorkingDir = workingDir
	if err := os.WriteFile(filepath.Join(workingDir, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	cookie, err := server.auth.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}

	request := func(target string) int {
		httpRequest := httptest.NewRequest(http.MethodGet, target, nil)
		httpRequest.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httpRequest)
		return response.Code
	}

	for _, target := range []string{
		"/api/files/content?path=" + url.QueryEscape(filepath.Join(workingDir, "inside.txt")),
		"/api/files/content?path=" + url.QueryEscape(outside),
		"/api/files?path=" + url.QueryEscape(filepath.Dir(workingDir)),
		"/api/files?path=" + url.QueryEscape("/etc"),
	} {
		if code := request(target); code != http.StatusOK {
			t.Fatalf("request %q status = %d, want 200", target, code)
		}
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/files?path="+url.QueryEscape(workingDir), nil)
	deleteRequest.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	deleteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusBadRequest {
		t.Fatalf("deleting the root status = %d, want 400", deleteResponse.Code)
	}
	if _, err := os.Stat(workingDir); err != nil {
		t.Fatalf("working directory was removed: %v", err)
	}
}

func TestUploadFileWritesAtomically(t *testing.T) {
	server, token := newTestServer(t)
	workingDir := t.TempDir()
	server.cfg.Terminal.WorkingDir = workingDir
	cookie, err := server.auth.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("path", workingDir); err != nil {
		t.Fatal(err)
	}
	part, err := form.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("hello from upload")); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/files/upload", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status code = %d, body = %q", response.Code, response.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(workingDir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello from upload" {
		t.Fatalf("uploaded contents = %q", data)
	}
	matches, err := filepath.Glob(filepath.Join(workingDir, ".termdock-upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary upload files remain: %v", matches)
	}
}

func TestUploadFileRejectsExistingDestination(t *testing.T) {
	server, token := newTestServer(t)
	workingDir := t.TempDir()
	server.cfg.Terminal.WorkingDir = workingDir
	finalPath := filepath.Join(workingDir, "existing.txt")
	if err := os.WriteFile(finalPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	cookie, err := server.auth.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "existing.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/files/upload", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status code = %d, body = %q", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("existing file was changed: %q", data)
	}
}

func TestTmuxAvailableUsesInjectedPathLookup(t *testing.T) {
	server, _ := newTestServer(t)
	var lookedUp string
	server.lookPath = func(name string) (string, error) {
		lookedUp = name
		return "/usr/bin/tmux", nil
	}

	if !server.tmuxAvailable() {
		t.Fatal("expected tmux to be available")
	}
	if lookedUp != "tmux" {
		t.Fatalf("looked up %q, want tmux", lookedUp)
	}
}

func TestTmuxUnavailableWhenPathLookupFails(t *testing.T) {
	server, _ := newTestServer(t)
	server.lookPath = func(string) (string, error) {
		return "", errors.New("tmux not found")
	}

	if server.tmuxAvailable() {
		t.Fatal("expected tmux to be unavailable")
	}
}
