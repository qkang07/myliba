package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const remoteChunkSize = 32 << 10

type wireMessage struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Path      string          `json:"path,omitempty"`
	Headers   http.Header     `json:"headers,omitempty"`
	Body      []byte          `json:"body,omitempty"`
	Status    int             `json:"status,omitempty"`
	Data      []byte          `json:"data,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Signal    json.RawMessage `json:"signal,omitempty"`
}

type remoteHub struct {
	mu                sync.Mutex
	conn              *websocket.Conn
	sendMu            sync.Mutex
	waiters           map[string]*remoteWaiter
	online            bool
	password          string
	secure            bool
	sessions          map[string]time.Time
	loginTries        int
	loginBlockedUntil time.Time
	webTraffic        trafficMeter
	nodeTraffic       trafficMeter
	iceServers        []iceServerConfig
	signalMu          sync.Mutex
	signalSessions    map[string]*websocket.Conn
}

type trafficSlot struct {
	second int64
	in     uint64
	out    uint64
}

type trafficRate struct {
	DownloadBps uint64 `json:"downloadBps"`
	UploadBps   uint64 `json:"uploadBps"`
}

// trafficMeter keeps a five-second rolling application-payload rate.
type trafficMeter struct {
	mu    sync.Mutex
	slots [5]trafficSlot
}

func (m *trafficMeter) add(inbound bool, count int) {
	if count <= 0 {
		return
	}
	second := time.Now().Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	slot := &m.slots[second%int64(len(m.slots))]
	if slot.second != second {
		*slot = trafficSlot{second: second}
	}
	if inbound {
		slot.in += uint64(count)
	} else {
		slot.out += uint64(count)
	}
}

func (m *trafficMeter) rate() trafficRate {
	now := time.Now().Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	var inbound, outbound uint64
	for _, slot := range m.slots {
		if slot.second >= now-4 && slot.second <= now {
			inbound += slot.in
			outbound += slot.out
		}
	}
	return trafficRate{DownloadBps: outbound / 5, UploadBps: inbound / 5}
}

type trafficBody struct {
	io.ReadCloser
	meter *trafficMeter
}

func (b trafficBody) Read(buffer []byte) (int, error) {
	count, err := b.ReadCloser.Read(buffer)
	b.meter.add(true, count)
	return count, err
}

type trafficResponseWriter struct {
	http.ResponseWriter
	meter *trafficMeter
}

func (w trafficResponseWriter) Write(data []byte) (int, error) {
	count, err := w.ResponseWriter.Write(data)
	w.meter.add(false, count)
	return count, err
}

func (w trafficResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *remoteHub) trackWebTraffic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/node" || r.URL.Path == "/signal" || r.URL.Path == "/api/session" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Body != nil {
			r.Body = trafficBody{ReadCloser: r.Body, meter: &h.webTraffic}
		}
		next.ServeHTTP(trafficResponseWriter{ResponseWriter: w, meter: &h.webTraffic}, r)
	})
}

type remoteWaiter struct {
	channel chan wireMessage
	done    chan struct{}
	once    sync.Once
}

func (w *remoteWaiter) finish() {
	w.once.Do(func() { close(w.done) })
}

func runNodeAgent(hubURL, token string, iceServers []iceServerConfig) {
	for {
		if err := connectNode(hubURL, token, iceServers); err != nil {
			log.Printf("Remote hub connection ended: %v", err)
		}
		time.Sleep(3 * time.Second)
	}
}

func validateHubURL(hubURL string) error {
	parsed, err := url.Parse(hubURL)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" {
		return errors.New("MYLIBA_HUB_URL must be a wss:// URL")
	}
	return nil
}

func connectNode(hubURL, token string, iceServers []iceServerConfig) error {
	parsed, err := url.Parse(hubURL)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" {
		return errors.New("MYLIBA_HUB_URL must be a wss:// URL")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/node"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, _, err := websocket.Dial(ctx, parsed.String(), &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "node stopped")
	conn.SetReadLimit(2 << 20)
	log.Printf("Connected to remote hub %s", parsed.Host)
	writeMu := &sync.Mutex{}
	rtc := newNodeRTCManager(conn, writeMu, iceServers)
	defer rtc.closeAll()
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					_ = conn.CloseNow()
					return
				}
			}
		}
	}()

	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var request wireMessage
		if err := json.Unmarshal(payload, &request); err != nil {
			continue
		}
		switch request.Type {
		case "request":
			if request.ID != "" {
				go handleNodeRequest(ctx, conn, writeMu, &request)
			}
		case "rtc-signal":
			rtc.handle(request.SessionID, request.Signal)
		}
	}
}

func handleNodeRequest(parent context.Context, conn *websocket.Conn, writeMu *sync.Mutex, input *wireMessage) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	defer cancel()
	target := "http://127.0.0.1:4317" + input.Path
	request, err := http.NewRequestWithContext(ctx, input.Method, target, bytes.NewReader(input.Body))
	if err != nil {
		_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "start", ID: input.ID, Status: http.StatusBadRequest})
		return
	}
	copyAllowedHeaders(request.Header, input.Headers, false)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "start", ID: input.ID, Status: http.StatusBadGateway})
		_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "chunk", ID: input.ID, Data: []byte("local resource service unavailable")})
		_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "end", ID: input.ID})
		return
	}
	defer response.Body.Close()
	if err := writeWire(conn, ctx, writeMu, wireMessage{Type: "start", ID: input.ID, Status: response.StatusCode, Headers: filteredHeaders(response.Header, true)}); err != nil {
		return
	}
	buffer := make([]byte, remoteChunkSize)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if err := writeWire(conn, ctx, writeMu, wireMessage{Type: "chunk", ID: input.ID, Data: buffer[:count]}); err != nil {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("Remote response stream interrupted: %v", readErr)
			}
			break
		}
	}
	_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "end", ID: input.ID})
}

type websocketWriter interface {
	Write(context.Context, websocket.MessageType, []byte) error
}

func writeWire(conn websocketWriter, ctx context.Context, lock *sync.Mutex, message wireMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	return conn.Write(ctx, websocket.MessageText, data)
}

func (h *remoteHub) attach(conn *websocket.Conn) {
	h.mu.Lock()
	previous := h.conn
	h.conn = conn
	h.online = true
	h.mu.Unlock()
	if previous != nil && previous != conn {
		_ = previous.Close(websocket.StatusPolicyViolation, "replaced by a newer node connection")
	}
	go h.readNode(conn)
}

func (h *remoteHub) readNode(conn *websocket.Conn) {
	defer func() {
		h.mu.Lock()
		if h.conn == conn {
			h.conn = nil
			h.online = false
			for _, waiter := range h.waiters {
				waiter.finish()
			}
			h.waiters = map[string]*remoteWaiter{}
		}
		h.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "node disconnected")
	}()
	for {
		_, payload, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		h.nodeTraffic.add(true, len(payload))
		var message wireMessage
		if json.Unmarshal(payload, &message) != nil {
			continue
		}
		if message.Type == "rtc-signal" {
			h.forwardRTCSignal(message.SessionID, message.Signal)
			continue
		}
		if message.ID == "" {
			continue
		}
		h.mu.Lock()
		waiter := h.waiters[message.ID]
		h.mu.Unlock()
		if waiter != nil {
			select {
			case waiter.channel <- message:
			case <-waiter.done:
			}
		}
	}
}

func (h *remoteHub) proxy(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api/session") {
		writeError(w, http.StatusNotFound, "接口不存在")
		return
	}
	if r.URL.Path == "/api/directories" {
		writeError(w, http.StatusForbidden, "目录浏览只能从家中电脑本机打开")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "不支持该请求方法")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "请求内容过大")
		return
	}
	h.mu.Lock()
	conn := h.conn
	if conn == nil {
		h.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "家中资源节点当前离线")
		return
	}
	id := newID()
	waiter := &remoteWaiter{channel: make(chan wireMessage, 8), done: make(chan struct{})}
	h.waiters[id] = waiter
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waiters, id)
		h.mu.Unlock()
		waiter.finish()
	}()

	path := r.URL.RequestURI()
	request := wireMessage{Type: "request", ID: id, Method: r.Method, Path: path, Headers: filteredHeaders(r.Header, false), Body: body}
	if err := h.send(conn, request); err != nil {
		writeError(w, http.StatusBadGateway, "无法联系家中资源节点")
		return
	}
	started := false
	for {
		select {
		case message := <-waiter.channel:
			switch message.Type {
			case "start":
				copyAllowedHeaders(w.Header(), message.Headers, true)
				w.WriteHeader(message.Status)
				started = true
			case "chunk":
				if !started {
					w.WriteHeader(http.StatusBadGateway)
					started = true
				}
				if _, err := w.Write(message.Data); err != nil {
					return
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			case "end":
				return
			}
		case <-waiter.done:
			if !started {
				writeError(w, http.StatusBadGateway, "家中资源节点连接已断开")
			}
			return
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Hour):
			if !started {
				writeError(w, http.StatusGatewayTimeout, "远程请求超时")
			}
			return
		}
	}
}

func (h *remoteHub) send(conn *websocket.Conn, message wireMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	err = conn.Write(context.Background(), websocket.MessageText, data)
	if err == nil {
		h.nodeTraffic.add(false, len(data))
	}
	return err
}

func filteredHeaders(headers http.Header, response bool) http.Header {
	allowed := []string{"Content-Type", "Range", "If-Modified-Since", "If-None-Match"}
	if response {
		allowed = []string{"Content-Type", "Content-Disposition", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag", "Cache-Control"}
	}
	result := make(http.Header)
	for _, name := range allowed {
		for _, value := range headers.Values(name) {
			result.Add(name, value)
		}
	}
	return result
}

func copyAllowedHeaders(destination, source http.Header, response bool) {
	for name, values := range filteredHeaders(source, response) {
		destination[name] = append([]string(nil), values...)
	}
}

func randomToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", data[:]), nil
}

func safeEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
