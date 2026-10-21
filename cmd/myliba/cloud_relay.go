package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// A storage relay carries only the authenticated cloud object protocol. The
// Hub is never a relay. Blocks have already been encrypted by the client.
const cloudRelayLimit = 18 << 20

type cloudRelayFrame struct {
	ID     string `json:"id"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status,omitempty"`
	Body   []byte `json:"body,omitempty"`
}

type cloudRelayConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan cloudRelayFrame
	done    chan struct{}
	once    sync.Once
}

func cloudRelayEndpointID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawPath != "" {
		return ""
	}
	const prefix = "/cloud/relay/"
	if !strings.HasPrefix(u.Path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(u.Path, prefix)
	if len(id) != 32 || strings.ToLower(id) != id {
		return ""
	}
	if _, err := hex.DecodeString(id); err != nil {
		return ""
	}
	return id
}

func (t *cloudRelayConn) close() {
	t.once.Do(func() {
		close(t.done)
		_ = t.conn.CloseNow()
	})
}

func (t *cloudRelayConn) write(ctx context.Context, frame cloudRelayFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.conn.Write(ctx, websocket.MessageBinary, data)
}

func cloudRelayPath(method, path string) bool {
	u, err := url.ParseRequestURI(path)
	if err != nil || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	if method == http.MethodGet && u.Path == "/cloud/inventory" {
		return u.RawQuery == "" || (len(u.Query()) == 1 && validCloudHash(u.Query().Get("after")))
	}
	if method == http.MethodGet && u.Path == "/cloud/catalog" {
		return u.RawQuery == ""
	}
	if u.RawQuery != "" || (method != http.MethodGet && method != http.MethodHead && method != http.MethodPut) {
		return false
	}
	parts := strings.Split(u.Path, "/")
	return len(parts) == 4 && parts[1] == "cloud" && (parts[2] == "blocks" || parts[2] == "catalog") && validCloudHash(parts[3])
}

func (c *cloudRuntime) relayRoutes(mux *http.ServeMux, token string) {
	mux.Handle("GET /cloud/tunnel/{id}", cloudBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if len(id) != 32 || cloudRelayEndpointID("https://relay.invalid/cloud/relay/"+id) == "" {
			http.Error(w, "invalid node", 400)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(cloudRelayLimit)
		tunnel := &cloudRelayConn{conn: conn, pending: map[string]chan cloudRelayFrame{}, done: make(chan struct{})}
		c.relayMu.Lock()
		previous := c.relays[id]
		c.relays[id] = tunnel
		c.relayMu.Unlock()
		if previous != nil {
			previous.close()
		}
		defer func() {
			c.relayMu.Lock()
			if c.relays[id] == tunnel {
				delete(c.relays, id)
			}
			c.relayMu.Unlock()
			tunnel.close()
		}()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame cloudRelayFrame
			if json.Unmarshal(data, &frame) != nil || frame.ID == "" || frame.Status < 100 || frame.Status > 599 || len(frame.Body) > cloudRelayLimit {
				return
			}
			tunnel.mu.Lock()
			pending := tunnel.pending[frame.ID]
			if pending != nil {
				delete(tunnel.pending, frame.ID)
			}
			tunnel.mu.Unlock()
			if pending != nil {
				pending <- frame
			}
		}
	}), token))

	mux.Handle("/cloud/relay/{id}/cloud/{tail...}", cloudBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := "/cloud/" + r.PathValue("tail")
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		if !cloudRelayPath(r.Method, path) {
			http.Error(w, "invalid cloud request", 400)
			return
		}
		c.relayMu.RLock()
		tunnel := c.relays[r.PathValue("id")]
		c.relayMu.RUnlock()
		if tunnel == nil {
			http.Error(w, "node offline", 503)
			return
		}
		limit := int64(0)
		if r.Method == http.MethodPut {
			limit = cloudMaxBlobSize
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil || int64(len(body)) > limit {
			http.Error(w, "request too large", 413)
			return
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			http.Error(w, "relay unavailable", 503)
			return
		}
		id := hex.EncodeToString(nonce[:])
		pending := make(chan cloudRelayFrame, 1)
		tunnel.mu.Lock()
		tunnel.pending[id] = pending
		tunnel.mu.Unlock()
		defer func() {
			tunnel.mu.Lock()
			delete(tunnel.pending, id)
			tunnel.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		if err := tunnel.write(ctx, cloudRelayFrame{ID: id, Method: r.Method, Path: path, Body: body}); err != nil {
			http.Error(w, "relay unavailable", 503)
			return
		}
		select {
		case response := <-pending:
			if r.Method == http.MethodGet && response.Status == 200 {
				w.Header().Set("Content-Type", "application/octet-stream")
			}
			w.WriteHeader(response.Status)
			if r.Method != http.MethodHead {
				_, _ = w.Write(response.Body)
			}
		case <-tunnel.done:
			http.Error(w, "node offline", 503)
		case <-ctx.Done():
			http.Error(w, "relay timeout", 504)
		}
	}), token))
}

type cloudRelayWriter struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *cloudRelayWriter) Header() http.Header { return w.header }
func (w *cloudRelayWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *cloudRelayWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(data) > cloudRelayLimit/2 {
		w.overflow = true
		return 0, errors.New("relay response too large")
	}
	return w.body.Write(data)
}

func (c *cloudRuntime) runRelayTunnel(ctx context.Context) {
	u, _ := url.Parse(c.relayURL)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/cloud/tunnel/" + c.id
	for ctx.Err() == nil {
		_, token, err := c.credentials()
		if err == nil && len(token) >= 32 {
			header := http.Header{"Authorization": []string{"Bearer " + token}}
			conn, _, dialErr := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: header})
			if dialErr == nil {
				conn.SetReadLimit(cloudRelayLimit)
				c.relayReady.Store(true)
				log.Printf("Cloud relay tunnel connected: %s", c.relayURL)
				c.serveRelayTunnel(ctx, conn, token)
				c.relayReady.Store(false)
				_ = conn.CloseNow()
			} else {
				log.Printf("Cloud relay tunnel: %v", dialErr)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *cloudRuntime) serveRelayTunnel(ctx context.Context, conn *websocket.Conn, token string) {
	var writeMu sync.Mutex
	sem := make(chan struct{}, 8)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame cloudRelayFrame
		if json.Unmarshal(data, &frame) != nil || frame.ID == "" || !cloudRelayPath(frame.Method, frame.Path) || len(frame.Body) > cloudMaxBlobSize {
			return
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		go func(frame cloudRelayFrame) {
			defer func() { <-sem }()
			req, err := http.NewRequestWithContext(ctx, frame.Method, frame.Path, bytes.NewReader(frame.Body))
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			writer := &cloudRelayWriter{header: make(http.Header)}
			mux := http.NewServeMux()
			c.disk.routes(mux, token)
			mux.ServeHTTP(writer, req)
			if writer.status == 0 {
				writer.status = 200
			}
			response := cloudRelayFrame{ID: frame.ID, Status: writer.status, Body: writer.body.Bytes()}
			if writer.overflow {
				response.Status, response.Body = 502, []byte("relay response too large")
			}
			payload, err := json.Marshal(response)
			if err != nil {
				return
			}
			writeMu.Lock()
			writeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			err = conn.Write(writeCtx, websocket.MessageBinary, payload)
			cancel()
			writeMu.Unlock()
			if err != nil {
				_ = conn.CloseNow()
			}
		}(frame)
	}
}
