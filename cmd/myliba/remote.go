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
	Type       string            `json:"type"`
	ID         string            `json:"id,omitempty"`
	Method     string            `json:"method,omitempty"`
	Path       string            `json:"path,omitempty"`
	Headers    http.Header       `json:"headers,omitempty"`
	Body       []byte            `json:"body,omitempty"`
	Status     int               `json:"status,omitempty"`
	Data       []byte            `json:"data,omitempty"`
	SessionID  string            `json:"sessionId,omitempty"`
	ShareID    string            `json:"shareId,omitempty"`
	Signal     json.RawMessage   `json:"signal,omitempty"`
	ICEServers []iceServerConfig `json:"iceServers,omitempty"`
}

// Browser RTC requests serialize Headers values as strings, while Go's
// WebSocket messages use string arrays. Accept both wire forms.
func (m *wireMessage) UnmarshalJSON(data []byte) error {
	type plain wireMessage
	var decoded struct {
		*plain
		Headers map[string]json.RawMessage `json:"headers"`
	}
	decoded.plain = (*plain)(m)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Headers != nil {
		m.Headers = make(http.Header, len(decoded.Headers))
		for name, value := range decoded.Headers {
			var values []string
			if len(value) > 0 && value[0] == '[' {
				if err := json.Unmarshal(value, &values); err != nil {
					return err
				}
			} else {
				var single string
				if err := json.Unmarshal(value, &single); err != nil {
					return err
				}
				values = []string{single}
			}
			m.Headers[http.CanonicalHeaderKey(name)] = values
		}
	}
	return nil
}

type remoteHub struct {
	mu                sync.Mutex
	conn              *websocket.Conn
	nodes             map[string]*remoteNode
	selectedNodes     map[string]string
	sendMu            sync.Mutex
	waiters           map[string]*remoteWaiter
	online            bool
	password          string
	secure            bool
	sessions          map[string]time.Time
	loginTries        int
	loginBlockedUntil time.Time
	shareChecks       int
	webTraffic        trafficMeter
	nodeTraffic       trafficMeter
	iceServers        []iceServerConfig
	signalMu          sync.Mutex
	signalSessions    map[string]signalSession
}

type remoteNode struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Kind string          `json:"kind"`
	Conn *websocket.Conn `json:"-"`
}

type signalSession struct {
	Client *websocket.Conn
	Node   *websocket.Conn
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
	node    *websocket.Conn
}

func (w *remoteWaiter) finish() {
	w.once.Do(func() { close(w.done) })
}

func runNodeAgent(ctx context.Context, hubURL, token, nodeID, nodeName string, iceServers []iceServerConfig, setStatus func(string)) {
	for {
		if ctx.Err() != nil {
			return
		}
		setStatus("connecting")
		if err := connectNode(ctx, hubURL, token, nodeID, nodeName, iceServers, func() { setStatus("connected") }); err != nil && ctx.Err() == nil {
			log.Printf("Remote hub connection ended: %v", err)
		}
		if ctx.Err() != nil {
			return
		}
		setStatus("disconnected")
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func validateHubURL(hubURL string) error {
	parsed, err := url.Parse(hubURL)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/" && parsed.Path != "/node") {
		return errors.New("MYLIBA_HUB_URL must be a wss:// URL ending in /node")
	}
	return nil
}

func connectNode(parent context.Context, hubURL, token, nodeID, nodeName string, iceServers []iceServerConfig, connected func()) error {
	parsed, err := url.Parse(hubURL)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" {
		return errors.New("MYLIBA_HUB_URL must be a wss:// URL")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/node"
	}
	query := parsed.Query()
	query.Set("id", nodeID)
	query.Set("name", nodeName)
	query.Set("kind", "desktop")
	parsed.RawQuery = query.Encode()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, _, err := websocket.Dial(ctx, parsed.String(), &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "node stopped")
	conn.SetReadLimit(2 << 20)
	log.Printf("Connected to remote hub %s", parsed.Host)
	connected()
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
			rtc.handle(request.SessionID, request.ShareID, request.Signal)
		}
	}
}

func handleNodeRequest(parent context.Context, conn *websocket.Conn, writeMu *sync.Mutex, input *wireMessage) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	defer cancel()
	if !allowedHubRelayRequest(input.Method, input.Path) {
		_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "start", ID: input.ID, Status: http.StatusForbidden})
		_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "end", ID: input.ID})
		return
	}
	target := "http://127.0.0.1:4317" + input.Path
	request, err := http.NewRequestWithContext(ctx, input.Method, target, bytes.NewReader(input.Body))
	if err != nil {
		_ = writeWire(conn, ctx, writeMu, wireMessage{Type: "start", ID: input.ID, Status: http.StatusBadRequest})
		return
	}
	copyAllowedHeaders(request.Header, input.Headers, false)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
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
	h.attachNode(conn, "legacy", "家中客户端", "desktop")
}

func (h *remoteHub) attachNode(conn *websocket.Conn, id, name, kind string) {
	h.mu.Lock()
	if h.nodes == nil {
		h.nodes = map[string]*remoteNode{}
	}
	previous := h.nodes[id]
	h.nodes[id] = &remoteNode{ID: id, Name: name, Kind: kind, Conn: conn}
	if kind == "desktop" && (h.conn == nil || id == "legacy") {
		h.conn = conn
		h.online = true
	}
	h.mu.Unlock()
	if previous != nil && previous.Conn != nil && previous.Conn != conn {
		_ = previous.Conn.Close(websocket.StatusPolicyViolation, "replaced by a newer connection for this node")
	}
	go h.readNode(id, conn)
	_ = h.send(conn, wireMessage{Type: "node-config", ICEServers: h.directICEServers()})
}

func (h *remoteHub) readNode(id string, conn *websocket.Conn) {
	defer func() {
		h.mu.Lock()
		if node := h.nodes[id]; node != nil && node.Conn == conn {
			node.Conn = nil
		}
		if h.conn == conn {
			h.conn = nil
			h.online = false
			for _, node := range h.nodes {
				if node.Conn != nil && node.Kind == "desktop" {
					h.conn = node.Conn
					h.online = true
					break
				}
			}
		}
		for key, waiter := range h.waiters {
			if waiter.node == conn {
				waiter.finish()
				delete(h.waiters, key)
			}
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
			h.forwardRTCSignal(conn, message.SessionID, message.Signal)
			continue
		}
		if message.ID == "" {
			continue
		}
		h.mu.Lock()
		waiter := h.waiters[message.ID]
		h.mu.Unlock()
		if waiter != nil && waiter.node == conn {
			select {
			case waiter.channel <- message:
			case <-waiter.done:
			}
		}
	}
}

func (h *remoteHub) proxy(w http.ResponseWriter, r *http.Request) {
	if !allowedHubRelayRequest(r.Method, r.URL.RequestURI()) {
		writeError(w, http.StatusForbidden, "远程访问不支持此操作")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "请求内容过大")
		return
	}
	h.mu.Lock()
	conn := h.selectedNodeLocked(r)
	if conn == nil {
		h.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "家中资源节点当前离线")
		return
	}
	id := newID()
	waiter := &remoteWaiter{channel: make(chan wireMessage, 8), done: make(chan struct{}), node: conn}
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
	allowed := map[string]bool{"Content-Type": true, "Range": true, "If-Modified-Since": true, "If-None-Match": true, "X-Myliba-Share-Key": true}
	if response {
		allowed = map[string]bool{"Content-Type": true, "Content-Disposition": true, "Content-Length": true, "Content-Range": true, "Accept-Ranges": true, "Last-Modified": true, "Etag": true, "Cache-Control": true, "Content-Security-Policy": true}
	}
	result := make(http.Header)
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if allowed[canonical] {
			for _, value := range values {
				result.Add(canonical, value)
			}
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

// allowedRemoteRequest is shared by the hub and the home node. The node must
// not treat a connected hub as permission to invoke every loopback API.
func allowedRemoteRequest(method, rawURI string) bool {
	parsed, err := url.ParseRequestURI(rawURI)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" || !strings.HasPrefix(rawURI, "/api/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\") {
			return false
		}
	}
	if len(parts) < 2 || parts[0] != "api" {
		return false
	}
	switch {
	case len(parts) == 4 && parts[1] == "shares" && parts[3] == "redeem":
		return method == http.MethodPost
	case len(parts) == 4 && parts[1] == "shares" && parts[3] == "file":
		return method == http.MethodGet
	case len(parts) == 5 && parts[1] == "shares" && parts[3] == "video":
		return method == http.MethodGet || method == http.MethodPost
	case len(parts) == 6 && parts[1] == "shares" && parts[3] == "video" && parts[5] == "init":
		return method == http.MethodGet
	case len(parts) == 7 && parts[1] == "shares" && parts[3] == "video" && parts[5] == "segments":
		return method == http.MethodGet
	case len(parts) == 3 && parts[1] == "shares":
		return method == http.MethodDelete
	case len(parts) == 4 && parts[1] == "resources" && parts[3] == "shares":
		return method == http.MethodGet || method == http.MethodPost
	case len(parts) == 3 && parts[1] == "cloud" && parts[2] == "files":
		return method == http.MethodGet
	case len(parts) == 5 && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "file":
		return method == http.MethodGet
	case len(parts) == 8 && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "assets" && parts[6] == "parts":
		return method == http.MethodGet
	case len(parts) == 6 && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "video":
		return method == http.MethodGet
	case len(parts) == 7 && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "video" && parts[6] == "init":
		return method == http.MethodGet
	case len(parts) == 8 && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "video" && parts[6] == "segments":
		return method == http.MethodGet
	case len(parts) == 5 && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "versions":
		return method == http.MethodGet
	case len(parts) == 7 && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "versions" && parts[6] == "file":
		return method == http.MethodGet
	case len(parts) == 2 && parts[1] == "preprocess":
		return method == http.MethodGet
	case len(parts) == 3 && parts[1] == "preprocess" && parts[2] == "prioritize":
		return method == http.MethodPost
	case len(parts) == 3 && parts[1] == "preprocess" && parts[2] == "retry-failed":
		return method == http.MethodPost
	case len(parts) == 4 && parts[1] == "preprocess" && parts[3] == "retry":
		return method == http.MethodPost
	case len(parts) == 2 && parts[1] == "roots":
		return method == http.MethodGet
	case len(parts) == 4 && parts[1] == "roots" && parts[3] == "scan":
		return method == http.MethodPost
	case len(parts) == 2 && parts[1] == "folders":
		return method == http.MethodGet || method == http.MethodPost
	case len(parts) == 3 && parts[1] == "folders":
		return method == http.MethodPut || method == http.MethodDelete
	case len(parts) == 3 && parts[1] == "resources" && parts[2] == "folder":
		return method == http.MethodPut
	case len(parts) == 2 && parts[1] == "resources":
		return method == http.MethodGet
	case len(parts) == 3 && parts[1] == "resources" && parts[2] == "selection":
		return method == http.MethodGet
	case len(parts) == 3 && parts[1] == "resources" && parts[2] != "tags":
		return method == http.MethodGet
	case len(parts) == 3 && parts[1] == "resources" && parts[2] == "tags":
		return method == http.MethodPost
	case len(parts) == 4 && parts[1] == "resources" && (parts[3] == "file" || parts[3] == "index" || parts[3] == "tag-suggestions" || parts[3] == "video-info"):
		return method == http.MethodGet
	case len(parts) == 5 && parts[1] == "resources" && parts[3] == "video":
		return method == http.MethodGet || method == http.MethodPost
	case len(parts) == 6 && parts[1] == "resources" && parts[3] == "video" && parts[5] == "init":
		return method == http.MethodGet
	case len(parts) == 7 && parts[1] == "resources" && parts[3] == "video" && parts[5] == "segments":
		return method == http.MethodGet
	case len(parts) == 4 && parts[1] == "resources" && parts[3] == "tags":
		return method == http.MethodPost
	case len(parts) == 4 && parts[1] == "resources" && parts[3] == "thumbnail":
		return method == http.MethodGet || method == http.MethodPost
	case len(parts) == 5 && parts[1] == "resources" && parts[3] == "tags":
		return method == http.MethodDelete
	case len(parts) == 2 && parts[1] == "collections":
		return method == http.MethodGet || method == http.MethodPost
	case len(parts) == 3 && parts[1] == "collections":
		return method == http.MethodPut || method == http.MethodDelete
	case len(parts) == 2 && (parts[1] == "tags" || parts[1] == "attribute-tags" || parts[1] == "tag-batch-suggestions"):
		return method == http.MethodGet
	default:
		return false
	}
}

// The Hub WebSocket is a control-plane fallback. File bytes and generated media
// must use a direct connection; this check also runs on the receiving node.
func allowedHubRelayRequest(method, rawURI string) bool {
	if !allowedRemoteRequest(method, rawURI) {
		return false
	}
	parsed, err := url.ParseRequestURI(rawURI)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "resources" &&
		(parts[3] == "file" || parts[3] == "thumbnail") {
		return false
	}
	if len(parts) >= 6 && parts[0] == "api" && parts[1] == "resources" && parts[3] == "video" {
		return false
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "shares" && parts[3] == "file" {
		return false
	}
	if len(parts) >= 6 && parts[0] == "api" && parts[1] == "shares" && parts[3] == "video" {
		return false
	}
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "file" {
		return false
	}
	if len(parts) >= 7 && parts[0] == "api" && parts[1] == "cloud" && parts[2] == "files" &&
		(parts[4] == "assets" || parts[4] == "video") {
		return false
	}
	if len(parts) == 7 && parts[0] == "api" && parts[1] == "cloud" && parts[2] == "files" && parts[4] == "versions" && parts[6] == "file" {
		return false
	}
	return true
}
