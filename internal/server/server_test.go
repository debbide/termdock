package server

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"webterm-cf/internal/auth"
	"webterm-cf/internal/config"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Security.CookieSecure = false
	manager, token, err := auth.New(time.Minute)
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
	request.Header.Set("Origin", "https://mc.bbe.pp.ua")
	request.Header.Set("X-Forwarded-Host", "mc.bbe.pp.ua")

	if !server.validOrigin(request) {
		t.Fatal("expected forwarded host to satisfy origin validation")
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

func TestLogoutKeepsTerminalSessionAndAllowsRelogin(t *testing.T) {
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
	terminalSession := &persistentSession{closed: make(chan struct{})}
	server.session = terminalSession

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

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", logoutResponse.Code)
	}
	if server.session != terminalSession {
		t.Fatal("logout unexpectedly removed the terminal session")
	}

	second := login()
	if second.Code != http.StatusNoContent {
		t.Fatalf("relogin status = %d", second.Code)
	}
}
