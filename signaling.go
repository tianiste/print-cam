package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

type broker struct {
	mu      sync.Mutex
	log     *slog.Logger
	hosts   map[string]*signalClient
	viewers map[string]map[string]*signalClient
}

func newBroker(logger *slog.Logger) *broker {
	return &broker{log: logger, hosts: make(map[string]*signalClient), viewers: make(map[string]map[string]*signalClient)}
}

func (a *app) handleHostWS(w http.ResponseWriter, r *http.Request) {
	a.handleWS(w, r, "host", strings.TrimPrefix(r.URL.Path, "/ws/host/"))
}

func (a *app) handleViewWS(w http.ResponseWriter, r *http.Request) {
	a.handleWS(w, r, "viewer", strings.TrimPrefix(r.URL.Path, "/ws/view/"))
}

func (a *app) handleWS(w http.ResponseWriter, r *http.Request, role, cameraID string) {
	if !a.checkOrigin(r) {
		http.Error(w, "bad origin", http.StatusForbidden)
		return
	}
	allowed, err := a.store.AllowRateLimit(r.Context(), "ws:"+clientIP(r), 30, time.Minute)
	if err != nil {
		http.Error(w, "rate limit failed", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	s, ok := a.currentSession(r)
	if !ok {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	_, err = a.store.Camera(r.Context(), s.UserID, cameraID)
	if err != nil {
		http.Error(w, "camera not found", http.StatusNotFound)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		a.log.Warn("websocket accept failed", "error", err)
		return
	}
	conn.SetReadLimit(maxSignalSize)
	client := newSignalClient(randomID(), s.UserID, cameraID, role, conn)
	if role == "host" {
		err = a.broker.addHost(client)
	} else {
		err = a.broker.addViewer(client)
	}
	if err != nil {
		client.send(signalMessage{Type: "error", Error: err.Error()})
		client.close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	a.audit(r.Context(), s.UserID, cameraID, role+"_connected")
	a.broker.readLoop(r.Context(), client)
	a.audit(context.Background(), s.UserID, cameraID, role+"_disconnected")
}

func (b *broker) addHost(c *signalClient) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing := b.hosts[c.cameraID]; existing != nil {
		return errors.New("camera already has an active host")
	}
	b.hosts[c.cameraID] = c
	c.send(signalMessage{Type: "host-ready"})
	return nil
}

func (b *broker) addViewer(c *signalClient) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hosts[c.cameraID] == nil {
		return errors.New("host tab is offline")
	}
	if b.viewers[c.cameraID] == nil {
		b.viewers[c.cameraID] = make(map[string]*signalClient)
	}
	b.viewers[c.cameraID][c.id] = c
	b.hosts[c.cameraID].send(signalMessage{Type: "viewer-join", ViewerID: c.id})
	return nil
}

func (b *broker) readLoop(ctx context.Context, c *signalClient) {
	defer b.remove(c)
	for {
		msg, err := c.read(ctx)
		if err != nil {
			return
		}
		if !validSignalType(msg.Type) {
			c.send(signalMessage{Type: "error", Error: "unknown signal type"})
			continue
		}
		b.relay(c, msg)
	}
}

func (b *broker) relay(c *signalClient, msg signalMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	msg.ViewerID = firstNonEmpty(msg.ViewerID, c.id)
	if c.role == "host" {
		viewers := b.viewers[c.cameraID]
		if viewers == nil {
			return
		}
		viewer := viewers[msg.ViewerID]
		if viewer != nil {
			viewer.send(msg)
		}
		return
	}
	host := b.hosts[c.cameraID]
	if host != nil {
		host.send(msg)
	}
}

func (b *broker) remove(c *signalClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c.role == "host" && b.hosts[c.cameraID] == c {
		delete(b.hosts, c.cameraID)
		for _, viewer := range b.viewers[c.cameraID] {
			viewer.send(signalMessage{Type: "host-left"})
			viewer.close(websocket.StatusNormalClosure, "host left")
		}
		delete(b.viewers, c.cameraID)
		return
	}
	if c.role == "viewer" {
		delete(b.viewers[c.cameraID], c.id)
		if b.hosts[c.cameraID] != nil {
			b.hosts[c.cameraID].send(signalMessage{Type: "viewer-left", ViewerID: c.id})
		}
	}
}

type signalClient struct {
	id       string
	userID   string
	cameraID string
	role     string
	conn     *websocket.Conn
	sendMu   sync.Mutex
}

func newSignalClient(id, userID, cameraID, role string, conn *websocket.Conn) *signalClient {
	return &signalClient{id: id, userID: userID, cameraID: cameraID, role: role, conn: conn}
}

func (c *signalClient) read(ctx context.Context) (signalMessage, error) {
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return signalMessage{}, err
	}
	var msg signalMessage
	err = json.Unmarshal(data, &msg)
	if err != nil {
		return signalMessage{}, err
	}
	return msg, nil
}

func (c *signalClient) send(msg signalMessage) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.conn.Write(ctx, websocket.MessageText, mustJSON(msg))
}

func (c *signalClient) close(code websocket.StatusCode, reason string) {
	c.conn.Close(code, reason)
}

type signalMessage struct {
	Type      string          `json:"type"`
	ViewerID  string          `json:"viewerId,omitempty"`
	SDP       json.RawMessage `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func validSignalType(t string) bool {
	switch t {
	case "viewer-join", "offer", "answer", "ice-candidate", "viewer-left", "host-left", "host-ready":
		return true
	default:
		return false
	}
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"type":"error","error":"json encode failed"}`)
	}
	return data
}
