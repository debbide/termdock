package server

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"webterm-cf/internal/terminal"
)

const terminalHistoryLimit = 1 << 20

// persistentSession owns the PTY independently from any browser connection.
// At most one WebSocket may be attached. All WebSocket writes are serialized
// through writeMu because gorilla/websocket permits one concurrent writer only.
type persistentSession struct {
	mu       sync.Mutex
	writeMu  sync.Mutex
	terminal *terminal.Terminal
	client   *websocket.Conn
	clientID uint64
	history  []byte
	closed   chan struct{}
	done     chan struct{}

	detachedAt time.Time
	retention  time.Duration
	wakeReaper chan struct{}
}

func newPersistentSession(ptySession *terminal.Terminal, retention time.Duration) *persistentSession {
	session := &persistentSession{
		terminal:   ptySession,
		closed:     make(chan struct{}),
		done:       make(chan struct{}),
		retention:  retention,
		wakeReaper: make(chan struct{}, 1),
	}
	go session.readOutput()
	if retention > 0 {
		go session.reapDetached()
	}
	return session
}

func (session *persistentSession) readOutput() {
	defer close(session.done)
	buffer := make([]byte, 32<<10)
	for {
		count, err := session.terminal.Read(buffer)
		if count > 0 {
			session.publish(buffer[:count])
		}
		if err != nil {
			session.close()
			return
		}
	}
}

func (session *persistentSession) publish(data []byte) {
	var client *websocket.Conn
	var clientID uint64

	session.mu.Lock()
	session.history = append(session.history, data...)
	if len(session.history) > terminalHistoryLimit {
		session.history = append([]byte(nil), session.history[len(session.history)-terminalHistoryLimit:]...)
	}
	client = session.client
	clientID = session.clientID
	session.mu.Unlock()

	if client != nil {
		if err := session.writeClient(client, clientID, websocket.BinaryMessage, data); err != nil {
			session.detach(client, clientID)
		}
	}
}

func (session *persistentSession) attach(client *websocket.Conn) (uint64, error) {
	session.mu.Lock()
	select {
	case <-session.closed:
		session.mu.Unlock()
		return 0, errors.New("terminal session is closed")
	default:
	}
	oldClient := session.client
	session.clientID++
	clientID := session.clientID
	session.client = client
	session.detachedAt = time.Time{}
	history := append([]byte(nil), session.history...)
	session.mu.Unlock()

	session.notifyReaper()
	if oldClient != nil && oldClient != client {
		_ = oldClient.Close()
	}
	if len(history) > 0 {
		if err := session.writeClient(client, clientID, websocket.BinaryMessage, history); err != nil {
			session.detach(client, clientID)
			return 0, err
		}
	}
	return clientID, nil
}

// detach includes the attachment generation so an old handler cannot detach a
// newer replacement connection that has already reattached to the same PTY.
func (session *persistentSession) detach(client *websocket.Conn, clientID uint64) {
	session.mu.Lock()
	if session.client == client && session.clientID == clientID {
		session.client = nil
		session.detachedAt = time.Now()
	}
	session.mu.Unlock()
	session.notifyReaper()
}

func (session *persistentSession) write(data []byte) error {
	_, err := session.terminal.Write(data)
	return err
}

func (session *persistentSession) resize(columns, rows uint16) error {
	return session.terminal.Resize(columns, rows)
}

func (session *persistentSession) ping(client *websocket.Conn, clientID uint64) error {
	return session.writeControl(client, clientID, websocket.PingMessage, nil)
}

func (session *persistentSession) writeClient(client *websocket.Conn, clientID uint64, messageType int, data []byte) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if !session.isCurrentClient(client, clientID) {
		return io.EOF
	}
	return client.WriteMessage(messageType, data)
}

func (session *persistentSession) writeControl(client *websocket.Conn, clientID uint64, messageType int, data []byte) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if !session.isCurrentClient(client, clientID) {
		return io.EOF
	}
	return client.WriteControl(messageType, data, time.Now().Add(5*time.Second))
}

func (session *persistentSession) isCurrentClient(client *websocket.Conn, clientID uint64) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.client == client && session.clientID == clientID
}

func (session *persistentSession) notifyReaper() {
	select {
	case session.wakeReaper <- struct{}{}:
	default:
	}
}

func (session *persistentSession) reapDetached() {
	var timer *time.Timer
	for {
		session.mu.Lock()
		detachedAt := session.detachedAt
		attached := session.client != nil
		session.mu.Unlock()

		if attached || detachedAt.IsZero() {
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			select {
			case <-session.closed:
				return
			case <-session.wakeReaper:
				continue
			}
		}

		remaining := time.Until(detachedAt.Add(session.retention))
		if remaining <= 0 {
			session.mu.Lock()
			stillDetached := session.client == nil && session.detachedAt.Equal(detachedAt)
			session.mu.Unlock()
			if stillDetached {
				session.close()
				return
			}
			continue
		}
		if timer == nil {
			timer = time.NewTimer(remaining)
		} else {
			timer.Reset(remaining)
		}
		select {
		case <-session.closed:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-session.wakeReaper:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (session *persistentSession) close() {
	session.mu.Lock()
	select {
	case <-session.closed:
		session.mu.Unlock()
		return
	default:
		close(session.closed)
	}
	client := session.client
	session.client = nil
	session.mu.Unlock()

	if client != nil {
		_ = client.Close()
	}
	_ = session.terminal.Close()
}
