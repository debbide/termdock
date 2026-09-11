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

type persistentSession struct {
	mu       sync.Mutex
	terminal *terminal.Terminal
	client   *websocket.Conn
	history  []byte
	closed   chan struct{}
	done     chan struct{}
}

func newPersistentSession(ptySession *terminal.Terminal) *persistentSession {
	session := &persistentSession{terminal: ptySession, closed: make(chan struct{}), done: make(chan struct{})}
	go session.readOutput()
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
	session.mu.Lock()
	session.history = append(session.history, data...)
	if len(session.history) > terminalHistoryLimit {
		session.history = append([]byte(nil), session.history[len(session.history)-terminalHistoryLimit:]...)
	}
	client := session.client
	if client != nil {
		if err := client.WriteMessage(websocket.BinaryMessage, data); err != nil {
			session.client = nil
		}
	}
	session.mu.Unlock()
}

func (session *persistentSession) attach(client *websocket.Conn) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	select {
	case <-session.closed:
		return errors.New("terminal session is closed")
	default:
	}
	if session.client != nil && session.client != client {
		_ = session.client.Close()
	}
	session.client = client
	if len(session.history) > 0 {
		return client.WriteMessage(websocket.BinaryMessage, session.history)
	}
	return nil
}

func (session *persistentSession) detach(client *websocket.Conn) {
	session.mu.Lock()
	if session.client == client {
		session.client = nil
	}
	session.mu.Unlock()
}

func (session *persistentSession) write(data []byte) error {
	_, err := session.terminal.Write(data)
	return err
}

func (session *persistentSession) resize(columns, rows uint16) error {
	return session.terminal.Resize(columns, rows)
}

func (session *persistentSession) ping(client *websocket.Conn) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.client != client {
		return io.EOF
	}
	return client.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
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
	if session.client != nil {
		_ = session.client.Close()
		session.client = nil
	}
	session.mu.Unlock()
	_ = session.terminal.Close()
}
