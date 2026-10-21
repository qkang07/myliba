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
	ID       string    `json:"id"`
	Endpoint string    `json:"endpoint"`
	Relayed  bool      `json:"relayed,omitempty"`
	Role     string    `json:"role"`
	SeenAt   time.Time `json:"seenAt"`
	cloudStorageStats
}

type cloudHub struct {
	mu            sync.Mutex
	nodes         map[string]cloudNode
	heartbeatSeen map[string]bool
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
	c := &cloudHub{nodes: map[string]cloudNode{}, heartbeatSeen: map[string]bool{}, disk: disk, registryPath: filepath.Join(dir, "cloud-nodes.json")}
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
	if relayID := cloudRelayEndpointID(node.Endpoint); (relayID != "" && node.ID != relayID) || (node.Relayed && node.Endpoint != "" && relayID == "") {
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

func (c *cloudHub) routes(mux *http.ServeMux, token string) {
	mux.Handle("POST /cloud/nodes/heartbeat", cloudBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var node cloudNode
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&node); err != nil || !validCloudNode(node) {
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
	}), token))
	mux.Handle("GET /cloud/nodes", cloudBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}), token))
	// The Hub keeps only opaque encrypted catalog records. It never stores blocks.
	mux.Handle("GET /cloud/catalog", cloudBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids, err := c.disk.list("catalog")
		if err != nil {
			http.Error(w, "catalog unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"records": ids})
	}), token))
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		method := method
		mux.Handle(method+" /cloud/catalog/{id}", cloudBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		}), token))
	}
}

func hubHTTPURL(wsURL string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" {
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
	default:
		return "", errors.New("invalid Hub scheme")
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}
