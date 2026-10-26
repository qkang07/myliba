package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type cloudNode struct {
	ID       string                     `json:"id"`
	Endpoint string                     `json:"endpoint"`
	LAN      []string                   `json:"lan,omitempty"`
	LANKey   string                     `json:"lanKey,omitempty"`
	Links    map[string]cloudPeerStatus `json:"links,omitempty"`
	Relayed  bool                       `json:"relayed,omitempty"`
	Role     string                     `json:"role"`
	SeenAt   time.Time                  `json:"seenAt"`
	cloudStorageStats
}

type cloudHub struct {
	mu            sync.Mutex
	nodes         map[string]cloudNode
	heartbeatSeen map[string]bool
	signals       map[string][]cloudPeerSignal
	signalWake    map[string]chan struct{}
	disk          *cloudDisk
	registryPath  string
}

func newCloudHub() (*cloudHub, error) {
	dir := strings.TrimSpace(os.Getenv("MYLIBA_HUB_DATA_DIR"))
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(base, "myliba-hub")
	}
	disk, err := newCloudDisk(filepath.Join(dir, "cloud"))
	if err != nil {
		return nil, err
	}
	c := &cloudHub{nodes: map[string]cloudNode{}, heartbeatSeen: map[string]bool{}, signals: map[string][]cloudPeerSignal{}, signalWake: map[string]chan struct{}{}, disk: disk, registryPath: filepath.Join(dir, "cloud-nodes.json")}
	if data, err := os.ReadFile(c.registryPath); err == nil {
		var saved []cloudNode
		if err := json.Unmarshal(data, &saved); err != nil {
			log.Printf("Cloud node registry unavailable: %v", err)
		} else {
			for _, node := range saved {
				if validCloudNode(node) && !node.SeenAt.IsZero() {
					c.nodes[node.ID] = node
				}
			}
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Cloud node registry unavailable: %v", err)
	}
	return c, nil
}

func validCloudNode(node cloudNode) bool {
	if !validCloudLAN(node.LAN, node.LANKey) {
		return false
	}
	if len(node.Links) > 64 {
		return false
	}
	for id, status := range node.Links {
		if !validCloudNodeID(id) || (status.State != "checking" && status.State != "connected" && status.State != "unreachable") || (status.Path != "" && status.Path != "lan" && status.Path != "webrtc" && status.Path != "direct") {
			return false
		}
	}
	if relayID := cloudRelayEndpointID(node.Endpoint); (relayID != "" && node.ID != relayID) || (node.Relayed && node.Endpoint != "" && relayID == "") {
		return false
	}
	if strings.HasPrefix(node.Endpoint, "p2p://") && node.Endpoint != "p2p://"+node.ID {
		return false
	}
	return node.ID != "" && len(node.ID) <= 128 && validCloudEndpoint(node.Endpoint) &&
		(node.Role == "client" || node.Role == "storage") &&
		(node.Backend == "" || node.Backend == "disk" || node.Backend == "s3" || node.Backend == "webdav" || node.Backend == "openlist") &&
		node.BlockCount >= 0 && node.CatalogCount >= 0 &&
		(node.BlockBytes == nil || *node.BlockBytes >= 0) &&
		(node.CapacityBytes == nil || node.AvailableBytes == nil || *node.AvailableBytes <= *node.CapacityBytes)
}

func (c *cloudHub) saveNodesLocked() error {
	nodes := make([]cloudNode, 0, len(c.nodes))
	for _, node := range c.nodes {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	data, err := json.Marshal(nodes)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(c.registryPath), ".cloud-nodes-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), c.registryPath)
}

const cloudOnlineWindow = 45 * time.Second

func (c *cloudHub) online(node cloudNode, now time.Time) bool {
	return c.heartbeatSeen[node.ID] && now.Sub(node.SeenAt) <= cloudOnlineWindow
}

func (c *cloudHub) removeOfflineNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		writeError(w, http.StatusBadRequest, "节点 ID 无效")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	node, exists := c.nodes[id]
	if !exists {
		writeError(w, http.StatusNotFound, "节点不存在")
		return
	}
	if c.online(node, time.Now().UTC()) {
		writeError(w, http.StatusConflict, "节点仍在线，不能移除")
		return
	}
	wasSeen := c.heartbeatSeen[id]
	delete(c.nodes, id)
	delete(c.heartbeatSeen, id)
	if err := c.saveNodesLocked(); err != nil {
		c.nodes[id] = node
		if wasSeen {
			c.heartbeatSeen[id] = true
		}
		writeError(w, http.StatusInternalServerError, "保存节点列表失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *cloudHub) overview(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	nodes := []struct {
		cloudNode
		Online bool `json:"online"`
	}{}
	var online, offline, knownCapacity, knownAvailable uint64
	var reportedBlockBytes int64
	var reportedBlockCount, unknownCapacity, unknownBlockBytes int64
	c.mu.Lock()
	for _, node := range c.nodes {
		active := c.online(node, now)
		nodes = append(nodes, struct {
			cloudNode
			Online bool `json:"online"`
		}{node, active})
		if !active {
			offline++
			continue
		}
		online++
		reportedBlockCount += node.BlockCount
		if node.BlockBytes == nil {
			unknownBlockBytes++
		} else {
			reportedBlockBytes += *node.BlockBytes
		}
		if node.CapacityBytes == nil || node.AvailableBytes == nil {
			unknownCapacity++
		} else {
			knownCapacity += *node.CapacityBytes
			knownAvailable += *node.AvailableBytes
		}
	}
	c.mu.Unlock()
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Online != nodes[j].Online {
			return nodes[i].Online
		}
		return nodes[i].ID < nodes[j].ID
	})
	catalog, err := c.disk.list("catalog")
	if err != nil {
		http.Error(w, "catalog unavailable", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"enabled": true, "nodes": nodes, "online": online, "offline": offline,
		"reportedBlockCount": reportedBlockCount, "reportedBlockBytes": reportedBlockBytes,
		"unknownBlockBytesNodes": unknownBlockBytes, "knownCapacityBytes": knownCapacity,
		"knownAvailableBytes": knownAvailable, "unknownCapacityNodes": unknownCapacity,
		"catalogRecords": len(catalog)})
}

func validCloudEndpoint(raw string) bool {
	if raw == "" {
		return true
	}
	if strings.HasPrefix(raw, "p2p://") {
		return validCloudNodeID(strings.TrimPrefix(raw, "p2p://"))
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/" && cloudRelayEndpointID(raw) == "") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		return true
	}
	return false
}

func validCloudNodeID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

type cloudPeerSignal struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Session string    `json:"session"`
	Type    string    `json:"type"`
	SDP     string    `json:"sdp"`
	Created time.Time `json:"-"`
}

func (c *cloudHub) signalRoutes(mux *http.ServeMux, authorize func(http.Handler) http.Handler) {
	mux.Handle("GET /cloud/ice", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		servers, err := loadICEServers()
		if err != nil {
			http.Error(w, "ICE unavailable", 503)
			return
		}
		writeJSON(w, 200, map[string]any{"servers": servers})
	})))
	mux.Handle("POST /cloud/signal", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var signal cloudPeerSignal
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&signal); err != nil || !validCloudNodeID(signal.From) || !validCloudNodeID(signal.To) || signal.From == signal.To || !validCloudNodeID(signal.Session) || (signal.Type != "offer" && signal.Type != "answer") || len(signal.SDP) > 100<<10 || len(signal.SDP) == 0 {
			http.Error(w, "invalid signal", 400)
			return
		}
		now := time.Now().UTC()
		c.mu.Lock()
		sender, senderOK := c.nodes[signal.From]
		target, targetOK := c.nodes[signal.To]
		if !senderOK || !targetOK || !c.online(sender, now) || !c.online(target, now) {
			c.mu.Unlock()
			http.Error(w, "node offline", 404)
			return
		}
		queue := c.signals[signal.To][:0]
		for _, entry := range c.signals[signal.To] {
			if now.Sub(entry.Created) < 60*time.Second {
				queue = append(queue, entry)
			}
		}
		if len(queue) >= 32 {
			c.mu.Unlock()
			http.Error(w, "signal queue full", 429)
			return
		}
		signal.Created = now
		c.signals[signal.To] = append(queue, signal)
		if wake := c.signalWake[signal.To]; wake != nil {
			close(wake)
			delete(c.signalWake, signal.To)
		}
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /cloud/signal/{id}", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validCloudNodeID(id) {
			http.Error(w, "invalid node", 400)
			return
		}
		c.mu.Lock()
		if len(c.signals[id]) == 0 && r.URL.Query().Get("wait") == "1" {
			wake := c.signalWake[id]
			if wake == nil {
				wake = make(chan struct{})
				c.signalWake[id] = wake
			}
			c.mu.Unlock()
			select {
			case <-wake:
			case <-time.After(20 * time.Second):
			case <-r.Context().Done():
				return
			}
			c.mu.Lock()
			if c.signalWake[id] == wake {
				delete(c.signalWake, id)
			}
		}
		messages := []cloudPeerSignal{}
		for _, entry := range c.signals[id] {
			if time.Since(entry.Created) < 60*time.Second {
				messages = append(messages, entry)
			}
		}
		delete(c.signals, id)
		c.mu.Unlock()
		writeJSON(w, 200, map[string]any{"messages": messages})
	})))
}

func (c *cloudHub) routes(mux *http.ServeMux, token string) {
	c.routesWithAuth(mux, func(next http.Handler) http.Handler { return cloudBearer(next, token) })
}

func (c *cloudHub) routesWithAuth(mux *http.ServeMux, authorize func(http.Handler) http.Handler) {
	c.signalRoutes(mux, authorize)
	mux.Handle("POST /cloud/nodes/heartbeat", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var node cloudNode
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&node); err != nil || !validCloudNode(node) {
			http.Error(w, "invalid node", 400)
			return
		}
		if cloudRelayEndpointID(node.Endpoint) != "" {
			node.Relayed = true
		}
		node.SeenAt = time.Now().UTC()
		c.mu.Lock()
		c.nodes[node.ID] = node
		c.heartbeatSeen[node.ID] = true
		err := c.saveNodesLocked()
		c.mu.Unlock()
		if err != nil {
			log.Printf("Cloud node registry save failed: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /cloud/nodes", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		nodes := []cloudNode{}
		c.mu.Lock()
		for _, node := range c.nodes {
			if !c.online(node, now) {
				continue
			}
			nodes = append(nodes, node)
		}
		c.mu.Unlock()
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
		writeJSON(w, 200, map[string]any{"nodes": nodes})
	})))
	// The Hub keeps only opaque encrypted catalog records. It never stores blocks.
	mux.Handle("GET /cloud/catalog", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids, err := c.disk.list("catalog")
		if err != nil {
			http.Error(w, "catalog unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"records": ids})
	})))
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		method := method
		mux.Handle(method+" /cloud/catalog/{id}", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if !validCloudHash(id) {
				http.Error(w, "invalid id", 400)
				return
			}
			if method == http.MethodPut {
				if err := c.disk.put("catalog", id, r.Body, cloudMaxRecordSize); err != nil {
					http.Error(w, "invalid catalog record", 400)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			file, err := c.disk.get("catalog", id)
			if err != nil {
				http.Error(w, "not found", 404)
				return
			}
			defer file.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeContent(w, r, id, time.Time{}, file)
		})))
	}
}

func hubHTTPURL(wsURL string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" || u.User != nil {
		return "", errors.New("invalid Hub URL")
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
			return "", errors.New("insecure Hub URL")
		}
		u.Scheme = "http"
	case "https":
	case "http":
		if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
			return "", errors.New("insecure Hub URL")
		}
	default:
		return "", errors.New("invalid Hub scheme")
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}
