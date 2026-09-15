package auth

import (
	"testing"
	"time"
)

func TestOneTimeTokenAndCookie(t *testing.T) {
	manager, token, err := New(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := manager.Exchange(token)
	if err != nil || !manager.Validate(cookie) {
		t.Fatal("valid token exchange failed")
	}
	if _, err := manager.Exchange(token); err == nil {
		t.Fatal("token replay succeeded")
	}
	if manager.Validate(cookie + "x") {
		t.Fatal("tampered cookie validated")
	}
}

func TestFixedTokenCanBeReused(t *testing.T) {
	manager, token, err := NewWithToken(time.Minute, "webterm-local-fixed-token")
	if err != nil {
		t.Fatal(err)
	}
	if token != "webterm-local-fixed-token" {
		t.Fatalf("unexpected token %q", token)
	}
	if _, err := manager.Exchange(token); err != nil {
		t.Fatal("fixed token exchange failed")
	}
	if _, err := manager.Exchange(token); err != nil {
		t.Fatal("fixed token reuse failed")
	}
}

func TestRevokeInvalidatesOnlyTheGivenCookie(t *testing.T) {
	manager, token, err := NewWithToken(time.Minute, "webterm-local-fixed-token")
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Exchange(token)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("issued cookies must not repeat")
	}

	manager.Revoke(first)
	if manager.Validate(first) {
		t.Fatal("revoked cookie is still valid")
	}
	if !manager.Validate(second) {
		t.Fatal("unrelated cookie was revoked")
	}
}

func TestRevokeAllInvalidatesEveryCookie(t *testing.T) {
	manager, token, err := NewWithToken(time.Minute, "webterm-local-fixed-token")
	if err != nil {
		t.Fatal(err)
	}
	cookies := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		cookie, err := manager.Exchange(token)
		if err != nil {
			t.Fatal(err)
		}
		cookies = append(cookies, cookie)
	}

	manager.RevokeAll()
	for _, cookie := range cookies {
		if manager.Validate(cookie) {
			t.Fatalf("cookie %q survived RevokeAll", cookie)
		}
	}
	if _, err := manager.Exchange(token); err != nil {
		t.Fatal("fixed token stopped working after RevokeAll")
	}
}

func TestRevokeIgnoresMalformedCookie(t *testing.T) {
	manager, _, err := NewWithToken(time.Minute, "webterm-local-fixed-token")
	if err != nil {
		t.Fatal(err)
	}
	manager.Revoke("not-a-cookie")
	manager.Revoke("")
	manager.Revoke("!!!.???")
}
