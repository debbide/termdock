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
