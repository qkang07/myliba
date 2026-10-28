package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type cloudPeerStatus struct {
	State     string                    `json:"state"`
	Path      string                    `json:"path,omitempty"`
	CheckedAt time.Time                 `json:"checkedAt,omitempty"`
	Checks    map[string]cloudPathCheck `json:"checks,omitempty"`
}

type cloudPathCheck struct {
	State         string `json:"state"`
	RTTMillis     int64  `json:"rttMillis,omitempty"`
	ConnectMillis int64  `json:"connectMillis,omitempty"`
	SpeedBps      int64  `json:"speedBps,omitempty"`
}

func (c *cloudRuntime) peerStatuses() map[string]cloudPeerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]cloudPeerStatus, len(c.peerStatus))
	for id, status := range c.peerStatus {
		result[id] = status
	}
	return result
}

func (c *cloudRuntime) probePeers(peers []cloudNode, token string) {
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, peer := range peers {
		if peer.ID == c.id || peer.Endpoint == "" && len(peer.LAN) == 0 {
			continue
		}
		peer := peer
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			status := cloudPeerStatus{State: "unreachable", CheckedAt: time.Now().UTC(), Checks: map[string]cloudPathCheck{}}
			if len(peer.LAN) > 0 {
				status.Checks["lan"] = c.checkCloudPath(ctx, false, func(path string) (*http.Response, error) {
					grant, err := c.peerGrant(ctx, token)
					if err != nil {
						return nil, err
					}
					return cloudLANRequest(ctx, peer, http.MethodGet, path, grant, nil)
				})
			}
			path, endpoint := "https", peer.Endpoint
			if strings.HasPrefix(endpoint, "p2p://") {
				path = "webrtc"
			}
			if peer.Relayed && strings.HasPrefix(endpoint, "https://") {
				path = "relay"
			}
			if endpoint != "" && (!strings.HasPrefix(endpoint, "https://") || len(peer.LAN) == 0 || endpoint != peer.LAN[0]) {
				status.Checks[path] = c.checkCloudPath(ctx, path == "webrtc", func(route string) (*http.Response, error) {
					if path == "webrtc" {
						return c.p2pRequest(ctx, http.MethodGet, peer.ID, route, token, nil)
					}
					grant, err := c.peerGrant(ctx, token)
					if err != nil {
						return nil, err
					}
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+route, nil)
					if err != nil {
						return nil, err
					}
					req.Header.Set("Authorization", "Bearer "+grant)
					return c.client.Do(req)
				})
			}
			for _, candidate := range []string{"lan", "webrtc", "https", "relay"} {
				if check, ok := status.Checks[candidate]; ok && check.State == "connected" {
					status.State, status.Path = "connected", candidate
					break
				}
			}
			status.CheckedAt = time.Now().UTC()
			c.mu.Lock()
			if c.peerStatus == nil {
				c.peerStatus = map[string]cloudPeerStatus{}
			}
			c.peerStatus[peer.ID] = status
			c.mu.Unlock()
		}()
	}
	wg.Wait()
}

func (c *cloudRuntime) checkCloudPath(ctx context.Context, negotiated bool, request func(string) (*http.Response, error)) cloudPathCheck {
	if negotiated {
		started := time.Now()
		resp, err := request("/cloud/diagnostic-payload?bytes=262144")
		if err != nil || resp == nil {
			return cloudPathCheck{State: "unreachable"}
		}
		count, copyErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		elapsed := time.Since(started)
		check := cloudPathCheck{State: "connected", ConnectMillis: elapsed.Milliseconds()}
		if resp.StatusCode != http.StatusOK || copyErr != nil || count == 0 {
			check.State = "unreachable"
			return check
		}
		if elapsed > 0 {
			check.SpeedBps = int64(float64(count) / elapsed.Seconds())
		}
		return check
	}
	var total time.Duration
	for i := 0; i < 3; i++ {
		started := time.Now()
		resp, err := request("/cloud/ping")
		if err != nil {
			return cloudPathCheck{State: "unreachable"}
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return cloudPathCheck{State: "unreachable"}
		}
		total += time.Since(started)
	}
	check := cloudPathCheck{State: "connected", RTTMillis: total.Milliseconds() / 3}
	started := time.Now()
	resp, err := request("/cloud/diagnostic-payload?bytes=262144")
	if err == nil && resp != nil {
		count, copyErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		elapsed := time.Since(started)
		if copyErr == nil && count > 0 && elapsed > 0 {
			check.SpeedBps = int64(float64(count) / elapsed.Seconds())
		}
	}
	return check
}
