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

type Manager struct {
	mu         sync.Mutex
	tokenHash  [32]byte
	tokenValid bool
	reusable   bool
	key        []byte
	lifetime   time.Duration
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
	return &Manager{tokenHash: sha256.Sum256([]byte(token)), tokenValid: true, reusable: true, key: key, lifetime: lifetime}, token, nil
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
	return manager.issue(time.Now().Add(manager.lifetime)), nil
}

func (manager *Manager) Validate(cookie string) bool {
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
	expires, err := time.Parse(time.RFC3339Nano, string(payload))
	return err == nil && time.Now().Before(expires)
}

func (manager *Manager) issue(expires time.Time) string {
	payload := []byte(expires.UTC().Format(time.RFC3339Nano))
	digest := hmac.New(sha256.New, manager.key)
	digest.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}
