package server

import (
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
