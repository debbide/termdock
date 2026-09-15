package server

import (
	"net"
	"net/http"
	"strings"
)

// clientAddress returns the direct peer address. Forwarding headers are never
// consulted here: they are client-controlled and would let a caller claim any
// address it likes.
func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil || host == "" {
		return strings.TrimSpace(request.RemoteAddr)
	}
	return host
}
