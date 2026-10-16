package daemon

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	// The phone app is a first-party client on the LAN; the bearer token is
	// the auth mechanism, not the Origin header.
	CheckOrigin: func(r *http.Request) bool { return true },
	// NOTE: Do NOT enable compression. The iPhone offers permessage-deflate,
	// but gorilla ignores the offer when this is false and sends plain frames,
	// which is the simplest interoperable path. (EnableCompression was tried
	// in v0.2.4 and the app still would not finish connecting.)
}

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 25 * time.Second
)

// Hub tracks live /ws connections and broadcasts ServerEvents.
type Hub struct {
	mu    sync.Mutex
	conns map[*wsConn]struct{}
}

// wsConn wraps one websocket connection with a serialized writer.
type wsConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func NewHub() *Hub { return &Hub{conns: make(map[*wsConn]struct{})} }

func (h *Hub) add(c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c] = struct{}{}
}

func (h *Hub) remove(c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
}

func (c *wsConn) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return c.ws.WriteJSON(v)
}

// Broadcast sends an event to every connected phone. Slow or dead clients are
// dropped rather than blocking the daemon.
func (h *Hub) Broadcast(v any) {
	h.mu.Lock()
	conns := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		if err := c.writeJSON(v); err != nil {
			h.remove(c)
			_ = c.ws.Close()
		}
	}
}

// serveWS upgrades the connection (auth already checked), pushes the current
// full snapshot, then keeps the socket alive with pings. The client sends a
// ping when idle; we answer with pongs via the read loop.
func (d *Daemon) serveWS(w http.ResponseWriter, r *http.Request) {
	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsConn{ws: ws}
	d.hub.add(c)
	defer func() {
		d.hub.remove(c)
		_ = ws.Close()
	}()

	// First frame: the current full snapshot.
	snap := d.store.Snapshot(d.sourceID)
	if err := c.writeJSON(ServerEvent{Snapshot: &snap}); err != nil {
		return
	}

	_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			c.mu.Lock()
			_ = ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
			err := ws.WriteMessage(websocket.PingMessage, nil)
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
	}
}

// broadcastSnapshot pushes the full snapshot to all /ws clients. It is wired
// as the store's onChange hook.
func (d *Daemon) broadcastSnapshot() {
	snap := d.store.Snapshot(d.sourceID)
	d.hub.Broadcast(ServerEvent{Snapshot: &snap})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
