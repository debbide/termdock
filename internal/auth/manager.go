package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
)

// cookieSeparator splits the session id from the signed expiry inside the
// cookie payload. It must not appear in base64url output or in RFC3339Nano.
const cookieSeparator = "~"

type Manager struct {
	mu         sync.Mutex
	tokenHash  [32]byte
	tokenValid bool
	reusable   bool
	key        []byte
	lifetime   time.Duration
	// sessions tracks the expiry of every issued cookie so an individual
	// cookie can be revoked before its signature expires.
	sessions map[string]time.Time
}

func New(lifetime time.Duration) (*Manager, string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, "", err
	}
	manager, token, err := NewWithToken(lifetime, base64.RawURLEncoding.EncodeToString(tokenBytes))
	if err != nil {
		return nil, "", err
	}
	manager.reusable = false
	return manager, token, nil
}

func NewWithToken(lifetime time.Duration, token string) (*Manager, string, error) {
	if strings.TrimSpace(token) == "" {
		return nil, "", errors.New("token must not be empty")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, "", err
	}
	return &Manager{tokenHash: sha256.Sum256([]byte(token)), tokenValid: true, reusable: true, key: key, lifetime: lifetime, sessions: make(map[string]time.Time)}, token, nil
}

func (manager *Manager) Exchange(token string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	hash := sha256.Sum256([]byte(token))
	if !manager.tokenValid || !hmac.Equal(hash[:], manager.tokenHash[:]) {
		return "", errors.New("invalid credentials")
	}
	if !manager.reusable {
		manager.tokenValid = false
	}
	return manager.issueLocked(time.Now().Add(manager.lifetime))
}

// Validate accepts a cookie only while its signature is intact, its expiry has
// not passed, and its session id has not been revoked.
func (manager *Manager) Validate(cookie string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.validateLocked(cookie, time.Now())
}

// Revoke invalidates one issued cookie. Logging out therefore retires the
// cookie on the server instead of only asking the browser to drop it.
func (manager *Manager) Revoke(cookie string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	identifier, ok := cookieIdentifier(cookie)
	if ok {
		delete(manager.sessions, identifier)
	}
}

// RevokeAll invalidates every issued cookie.
func (manager *Manager) RevokeAll() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.sessions = make(map[string]time.Time)
}

func (manager *Manager) validateLocked(cookie string, now time.Time) bool {
	parts := strings.Split(cookie, ".")
	if len(parts) != 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	digest := hmac.New(sha256.New, manager.key)
	digest.Write(payload)
	if !hmac.Equal(signature, digest.Sum(nil)) {
		return false
	}
	identifier, expires, ok := splitPayload(string(payload))
	if !ok || !now.Before(expires) {
		return false
	}
	expiresAt, ok := manager.sessions[identifier]
	return ok && now.Before(expiresAt)
}

func (manager *Manager) issueLocked(expires time.Time) (string, error) {
	manager.pruneLocked(time.Now())
	identifierBytes := make([]byte, 16)
	if _, err := rand.Read(identifierBytes); err != nil {
		return "", err
	}
	identifier := base64.RawURLEncoding.EncodeToString(identifierBytes)
	manager.sessions[identifier] = expires
	payload := []byte(identifier + cookieSeparator + expires.UTC().Format(time.RFC3339Nano))
	digest := hmac.New(sha256.New, manager.key)
	digest.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(digest.Sum(nil)), nil
}

func (manager *Manager) pruneLocked(now time.Time) {
	for identifier, expires := range manager.sessions {
		if !now.Before(expires) {
			delete(manager.sessions, identifier)
		}
	}
}

func cookieIdentifier(cookie string) (string, bool) {
	parts := strings.Split(cookie, ".")
	if len(parts) != 2 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	identifier, _, ok := splitPayload(string(payload))
	return identifier, ok
}

func splitPayload(payload string) (string, time.Time, bool) {
	index := strings.Index(payload, cookieSeparator)
	if index <= 0 || index == len(payload)-1 {
		return "", time.Time{}, false
	}
	expires, err := time.Parse(time.RFC3339Nano, payload[index+1:])
	if err != nil {
		return "", time.Time{}, false
	}
	return payload[:index], expires, true
}
