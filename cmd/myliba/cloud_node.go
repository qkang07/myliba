package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type cloudRuntime struct {
	disk         *cloudDisk
	id           string
	role         string
	endpoint     string
	db           *sql.DB
	hubURL       string
	token        string
	mu           sync.RWMutex
	peers        []cloudNode
	lan          []string
	lanPort      string
	lanKey       string
	lanCert      tls.Certificate
	peerStatus   map[string]cloudPeerStatus
	bootstrap    []cloudNode
	locations    map[string][]cloudNode
	client       *http.Client
	relayURL     string
	relayOn      bool
	relayMu      sync.RWMutex
	relays       map[string]*cloudRelayConn
	relayReady   atomic.Bool
	signalOnce   sync.Once
	signalMu     sync.Mutex
	answers      map[string]chan cloudPeerSignal
	grantForPeer func(context.Context, string) (string, error)
}

func cloudDataDir(storageOnly bool) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("MYLIBA_CLOUD_DATA_DIR")); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if storageOnly {
		return filepath.Join(base, "myliba-storage"), nil
	}
	return filepath.Join(base, "myliba", "cloud"), nil
}

func readOrCreateCloudID(dir string) (string, error) {
	path := filepath.Join(dir, "node-id")
	if raw, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(raw))
		if len(id) == 32 && cloudRelayEndpointID("https://relay.invalid/cloud/relay/"+id) == id {
			return id, nil
		}
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	if err := os.WriteFile(path, []byte(id), 0600); err != nil {
		return "", err
	}
	return id, nil
}

func newCloudRuntime(db *sql.DB, storageOnly bool) (*cloudRuntime, error) {
	dir, err := cloudDataDir(storageOnly)
	if err != nil {
		return nil, err
	}
	disk, err := newCloudDisk(dir)
	if err != nil {
		return nil, err
	}
	if storageOnly {
		disk.backend, err = newCloudBackendFromEnv()
		if err != nil {
			return nil, err
		}
	}
	id, err := readOrCreateCloudID(dir)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("MYLIBA_CLOUD_PUBLIC_URL")), "/")
	relayURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MYLIBA_CLOUD_RELAY_URL")), "/")
	if relayURL != "" && (!validCloudEndpoint(relayURL) || strings.Contains(relayURL, "/cloud/relay/")) {
		return nil, errors.New("MYLIBA_CLOUD_RELAY_URL must be a storage node HTTPS root URL")
	}
	if endpoint == "" && relayURL != "" {
		endpoint = relayURL + "/cloud/relay/" + id
	}
	if !validCloudEndpoint(endpoint) {
		return nil, errors.New("MYLIBA_CLOUD_PUBLIC_URL must be HTTPS, or loopback HTTP for development")
	}
	role := "client"
	if storageOnly {
		role = "storage"
	}
	rt := &cloudRuntime{
		disk: disk, id: id, role: role, endpoint: endpoint, db: db,
		client: &http.Client{
			Timeout:       90 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		locations: map[string][]cloudNode{}, peerStatus: map[string]cloudPeerStatus{}, answers: map[string]chan cloudPeerSignal{}, relayURL: relayURL,
		relayOn: os.Getenv("MYLIBA_CLOUD_RELAY_ENABLED") == "1", relays: map[string]*cloudRelayConn{},
	}
	rt.lanCert, rt.lanKey, err = cloudLANCertificate(dir)
	if err != nil {
		log.Printf("Cloud LAN unavailable: %v", err)
	}
	if raw := strings.TrimSpace(os.Getenv("MYLIBA_CLOUD_BOOTSTRAP_URLS")); raw != "" {
		var endpoints []string
		if err := json.Unmarshal([]byte(raw), &endpoints); err != nil {
			return nil, errors.New("MYLIBA_CLOUD_BOOTSTRAP_URLS must be a JSON URL array")
		}
		for _, endpoint := range endpoints {
			endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
			if endpoint == "" || !validCloudEndpoint(endpoint) {
				return nil, errors.New("invalid cloud bootstrap URL")
			}
			if endpoint != rt.endpoint {
				rt.bootstrap = append(rt.bootstrap, cloudNode{ID: cloudHash([]byte(endpoint)), Endpoint: endpoint, Role: "storage"})
			}
		}
		rt.peers = append([]cloudNode(nil), rt.bootstrap...)
	}
	if storageOnly {
		rt.hubURL = strings.TrimSpace(os.Getenv("MYLIBA_HUB_URL"))
		if endpoint == "" {
			return nil, errors.New("storage node needs MYLIBA_CLOUD_PUBLIC_URL")
		}
		if rt.hubURL == "" {
			return nil, errors.New("storage node needs MYLIBA_HUB_URL")
		}
		credentialPath := filepath.Join(dir, "device-credential")
		if raw, readErr := os.ReadFile(credentialPath); readErr == nil {
			rt.token = strings.TrimSpace(string(raw))
		}
		if len(rt.token) < 32 {
			password := os.Getenv("MYLIBA_OWNER_PASSWORD")
			if password == "" {
				return nil, errors.New("storage node needs MYLIBA_OWNER_PASSWORD for first pairing")
			}
			rt.token, err = pairCloudStorage(rt.hubURL, rt.id, password)
			if err != nil {
				return nil, fmt.Errorf("storage node pairing: %w", err)
			}
			if err = os.WriteFile(credentialPath, []byte(rt.token), 0600); err != nil {
				return nil, err
			}
		}
	}
	return rt, nil
}

func (c *cloudRuntime) credentials() (string, string, error) {
	if c.db == nil {
		return c.hubURL, c.token, nil
	}
	var rawURL, token string
	err := c.db.QueryRow(`SELECT p.url,p.token FROM hub_profiles p JOIN hub_settings s ON s.value=p.id WHERE s.key='active_hub'`).Scan(&rawURL, &token)
	if err == sql.ErrNoRows || (err != nil && strings.Contains(err.Error(), "no such table: hub_profiles")) {
		return "", "", nil
	}
	return rawURL, token, err
}

func (c *cloudRuntime) peerGrant(ctx context.Context, deviceToken string) (string, error) {
	if c.grantForPeer != nil {
		return c.grantForPeer(ctx, deviceToken)
	}
	wsURL, _, err := c.credentials()
	if err != nil {
		return "", err
	}
	base, err := hubHTTPURL(wsURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/cloud/auth/grant", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Hub cloud grant: HTTP %d", resp.StatusCode)
	}
	var grant struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&grant); err != nil {
		return "", err
	}
	if len(grant.Token) < 32 || strings.ContainsAny(grant.Token, "\r\n") {
		return "", errors.New("invalid cloud grant")
	}
	return grant.Token, nil
}

func pairCloudStorage(rawHub, deviceID, password string) (string, error) {
	base, err := hubHTTPURL(rawHub)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid Hub URL")
	}
	origin := parsed.Scheme + "://" + parsed.Host
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 15 * time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, input any, output any) error {
		body, _ := json.Marshal(input)
		req, err := http.NewRequest(http.MethodPost, origin+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("Hub HTTP %d", resp.StatusCode)
		}
		if output != nil {
			return json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(output)
		}
		return nil
	}
	if err := post("/api/session", map[string]string{"password": password}, nil); err != nil {
		return "", err
	}
	defer func() {
		req, err := http.NewRequest(http.MethodDelete, origin+"/api/session", nil)
		if err == nil {
			req.Header.Set("Origin", origin)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}()
	var paired struct {
		Token string `json:"token"`
	}
	if err := post("/api/hub/devices", map[string]any{"id": deviceID, "name": "云层存储节点", "kind": "storage", "canCloud": true}, &paired); err != nil {
		return "", err
	}
	if len(paired.Token) < 32 || strings.ContainsAny(paired.Token, "\r\n") {
		return "", errors.New("Hub returned invalid device credential")
	}
	return paired.Token, nil
}

func (c *cloudRuntime) authorizeCloudPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		wsURL, self, err := c.credentials()
		if err != nil || len(self) < 32 {
			http.Error(w, "cloud credentials unavailable", http.StatusServiceUnavailable)
			return
		}
		base, err := hubHTTPURL(wsURL)
		if err != nil {
			http.Error(w, "Hub unavailable", http.StatusServiceUnavailable)
			return
		}
		body, _ := json.Marshal(map[string]string{"token": strings.TrimPrefix(auth, "Bearer ")})
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		check, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/cloud/auth/verify", bytes.NewReader(body))
		if err != nil {
			http.Error(w, "Hub unavailable", http.StatusServiceUnavailable)
			return
		}
		check.Header.Set("Authorization", "Bearer "+self)
		check.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(check)
		if err != nil {
			http.Error(w, "Hub unavailable", http.StatusServiceUnavailable)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c *cloudRuntime) request(ctx context.Context, method, base, path, token string, body io.Reader) (*http.Response, error) {
	if !validCloudEndpoint(base) || base == "" {
		return nil, errors.New("invalid cloud endpoint")
	}
	wsURL, _, err := c.credentials()
	if err != nil {
		return nil, err
	}
	hubBase := ""
	if wsURL != "" {
		hubBase, err = hubHTTPURL(wsURL)
		if err != nil {
			return nil, err
		}
	}
	var lanPeer cloudNode
	if strings.TrimRight(base, "/") != hubBase {
		c.mu.RLock()
		for _, peer := range c.peers {
			if peer.Endpoint == base {
				lanPeer = peer
				break
			}
		}
		c.mu.RUnlock()
	}
	if len(lanPeer.LAN) > 0 {
		payload, err := readCloudRequestBody(body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(payload)
		if grant, grantErr := c.peerGrant(ctx, token); grantErr == nil {
			if response, lanErr := cloudLANRequest(ctx, lanPeer, method, path, grant, payload); lanErr == nil {
				return response, nil
			}
		}
	}
	if strings.HasPrefix(base, "p2p://") {
		return c.p2pRequest(ctx, method, strings.TrimPrefix(base, "p2p://"), path, token, body)
	}
	if strings.TrimRight(base, "/") != hubBase {
		token, err = c.peerGrant(ctx, token)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.client.Do(req)
}

func (c *cloudRuntime) put(ctx context.Context, base, kind, id, token string, data []byte) error {
	response, err := c.request(ctx, http.MethodPut, base, "/cloud/"+kind+"/"+id, token, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloud %s PUT: HTTP %d", kind, response.StatusCode)
	}
	return nil
}

func (c *cloudRuntime) get(ctx context.Context, base, kind, id, token string, limit int64) ([]byte, error) {
	response, err := c.request(ctx, http.MethodGet, base, "/cloud/"+kind+"/"+id, token, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud %s GET: HTTP %d", kind, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || cloudHash(data) != id {
		return nil, errors.New("cloud object failed verification")
	}
	return data, nil
}

func (c *cloudRuntime) remoteIDs(ctx context.Context, base, token string) ([]string, error) {
	response, err := c.request(ctx, http.MethodGet, base, "/cloud/catalog", token, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog list: HTTP %d", response.StatusCode)
	}
	var list struct {
		Records []string `json:"records"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&list); err != nil {
		return nil, err
	}
	for _, id := range list.Records {
		if !validCloudHash(id) {
			return nil, errors.New("invalid remote catalog ID")
		}
	}
	return list.Records, nil
}

func (c *cloudRuntime) syncCatalog(ctx context.Context, base, token string) error {
	remote, err := c.remoteIDs(ctx, base, token)
	if err != nil {
		return err
	}
	local, err := c.disk.list("catalog")
	if err != nil {
		return err
	}
	localSet, remoteSet := map[string]bool{}, map[string]bool{}
	for _, id := range local {
		if c.disk.valid("catalog", id) {
			localSet[id] = true
		}
	}
	for _, id := range remote {
		remoteSet[id] = true
	}
	for _, id := range remote {
		if localSet[id] {
			continue
		}
		data, err := c.get(ctx, base, "catalog", id, token, cloudMaxRecordSize)
		if err == nil {
			err = c.disk.put("catalog", id, bytes.NewReader(data), cloudMaxRecordSize)
		}
		if err != nil {
			return err
		}
	}
	for _, id := range local {
		if !localSet[id] {
			continue
		}
		if remoteSet[id] {
			continue
		}
		file, err := c.disk.get("catalog", id)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, cloudMaxRecordSize+1))
		file.Close()
		if err != nil {
			return err
		}
		if err := c.put(ctx, base, "catalog", id, token, data); err != nil {
			return err
		}
	}
	return nil
}

func (c *cloudRuntime) activePeers() []cloudNode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := append([]cloudNode{}, c.peers...)
	return result
}

func (c *cloudRuntime) blockLocations(id string) []cloudNode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]cloudNode(nil), c.locations[id]...)
}

func (c *cloudRuntime) peerInventory(ctx context.Context, peer cloudNode, token string) ([]string, error) {
	ids := []string{}
	after := ""
	for {
		path := "/cloud/inventory"
		if after != "" {
			path += "?after=" + after
		}
		response, err := c.request(ctx, http.MethodGet, peer.Endpoint, path, token, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Blocks []string `json:"blocks"`
			Next   string   `json:"next"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&page)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			return nil, errors.New("inventory unavailable")
		}
		for _, id := range page.Blocks {
			if !validCloudHash(id) || (after != "" && id <= after) {
				return nil, errors.New("invalid inventory")
			}
			after = id
			ids = append(ids, id)
		}
		if page.Next == "" {
			return ids, nil
		}
		if page.Next != after || len(page.Blocks) == 0 {
			return nil, errors.New("invalid inventory cursor")
		}
	}
}

func (c *cloudRuntime) refreshLocations(ctx context.Context, token string, peers []cloudNode) {
	locations := map[string][]cloudNode{}
	if ids, err := c.disk.list("blocks"); err == nil {
		self := cloudNode{ID: c.id, Endpoint: c.endpoint, Role: c.role}
		for _, id := range ids {
			locations[id] = append(locations[id], self)
		}
	}
	for _, peer := range peers {
		ids, err := c.peerInventory(ctx, peer, token)
		if err != nil {
			continue
		}
		for _, id := range ids {
			locations[id] = append(locations[id], peer)
		}
	}
	c.mu.Lock()
	c.locations = locations
	c.mu.Unlock()
}

func (c *cloudRuntime) syncOnce(ctx context.Context) error {
	wsURL, token, err := c.credentials()
	if err != nil {
		return err
	}
	if wsURL == "" && len(c.bootstrap) == 0 {
		c.mu.Lock()
		c.peers = nil
		c.mu.Unlock()
		c.refreshLocations(ctx, "", nil)
		return nil
	}
	if len(token) < 32 {
		return errors.New("cloud peers require an authorized device credential")
	}
	peers := c.activePeers()
	known := map[string]bool{}
	for _, peer := range peers {
		known[peer.Endpoint] = true
	}
	for _, peer := range c.bootstrap {
		if !known[peer.Endpoint] {
			peers = append(peers, peer)
			known[peer.Endpoint] = true
		}
	}
	var hubErr error
	if wsURL != "" {
		hub, parseErr := hubHTTPURL(wsURL)
		if parseErr != nil {
			hubErr = parseErr
		} else {
			var discovered []cloudNode
			discovered, hubErr = c.hubPeers(ctx, hub, token)
			if hubErr == nil {
				peers = append([]cloudNode(nil), c.bootstrap...)
				known = map[string]bool{}
				for _, peer := range peers {
					known[peer.Endpoint] = true
				}
			}
			for _, peer := range discovered {
				if !known[peer.Endpoint] {
					peers = append(peers, peer)
					known[peer.Endpoint] = true
				}
			}
			if hubErr == nil {
				hubErr = c.syncCatalog(ctx, hub, token)
			}
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	c.mu.Lock()
	c.peers = peers
	if c.peerStatus == nil {
		c.peerStatus = map[string]cloudPeerStatus{}
	}
	active := make(map[string]bool, len(peers))
	for _, peer := range peers {
		active[peer.ID] = true
		if _, exists := c.peerStatus[peer.ID]; !exists {
			c.peerStatus[peer.ID] = cloudPeerStatus{State: "checking"}
		}
	}
	for id := range c.peerStatus {
		if !active[id] {
			delete(c.peerStatus, id)
		}
	}
	c.mu.Unlock()
	go c.probePeers(peers, token)
	for _, peer := range peers {
		if err := c.syncCatalog(ctx, peer.Endpoint, token); err != nil {
			log.Printf("Cloud index sync with %s: %v", peer.ID, err)
		}
	}
	c.refreshLocations(ctx, token, peers)
	c.repairBlocks(ctx, token, peers)
	return hubErr
}

func (c *cloudRuntime) hubPeers(ctx context.Context, hub, token string) ([]cloudNode, error) {
	stats, err := c.disk.stats()
	if err != nil {
		log.Printf("Cloud storage stats unavailable: %v", err)
	}
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = "p2p://" + c.id
	}
	if c.relayURL != "" && cloudRelayEndpointID(endpoint) != "" && !c.relayReady.Load() {
		endpoint = ""
	}
	c.mu.Lock()
	if c.lanPort != "" {
		c.lan = cloudLANAddresses(c.lanPort)
	}
	lan := append([]string(nil), c.lan...)
	c.mu.Unlock()
	lanKey := c.lanKey
	if len(lan) == 0 {
		lanKey = ""
	}
	links := c.peerStatuses()
	for id := range links {
		if !validCloudNodeID(id) {
			delete(links, id)
		}
	}
	data, _ := json.Marshal(cloudNode{ID: c.id, Endpoint: endpoint, LAN: lan, LANKey: lanKey, Links: links, Relayed: cloudRelayEndpointID(c.endpoint) != "", Role: c.role, cloudStorageStats: stats})
	response, err := c.request(ctx, http.MethodPost, hub, "/cloud/nodes/heartbeat", token, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("Hub registration: HTTP %d", response.StatusCode)
	}
	response, err = c.request(ctx, http.MethodGet, hub, "/cloud/nodes", token, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Hub node list: HTTP %d", response.StatusCode)
	}
	var list struct {
		Nodes []cloudNode `json:"nodes"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&list); err != nil {
		return nil, err
	}
	peers := []cloudNode{}
	for _, node := range list.Nodes {
		if node.ID != c.id && (node.Endpoint != "" || len(node.LAN) > 0) && validCloudEndpoint(node.Endpoint) && validCloudLAN(node.LAN, node.LANKey) {
			if node.Endpoint == "" {
				node.Endpoint = node.LAN[0]
			}
			peers = append(peers, node)
		}
	}
	return peers, nil
}

func (c *cloudRuntime) run(ctx context.Context) {
	c.startLAN(ctx)
	c.startSignalInbox(ctx)
	if c.relayURL != "" && strings.Contains(c.endpoint, "/cloud/relay/") {
		go c.runRelayTunnel(ctx)
	}
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := c.syncOnce(attempt); err != nil {
			log.Printf("Cloud sync: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *cloudRuntime) directHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, token, err := c.credentials()
		if err != nil || len(token) < 32 {
			http.Error(w, "unavailable", 503)
			return
		}
		inner := http.NewServeMux()
		c.disk.routesWithAuth(inner, c.authorizeCloudPeer)
		if c.relayOn {
			c.relayRoutesWithAuth(inner, c.authorizeCloudPeer)
		}
		inner.ServeHTTP(w, r)
	}))
	return mux
}

func (c *cloudRuntime) serveDirect() error {
	addr := strings.TrimSpace(os.Getenv("MYLIBA_CLOUD_LISTEN"))
	if addr == "" {
		addr = "127.0.0.1:4319"
	}
	log.Printf("Cloud direct endpoint listening on %s", addr)
	return (&http.Server{Addr: addr, Handler: c.directHandler(), ReadHeaderTimeout: 10 * time.Second}).ListenAndServe()
}

func runCloudStorage() error {
	c, err := newCloudRuntime(nil, true)
	if err != nil {
		return err
	}
	go c.run(context.Background())
	return c.serveDirect()
}
