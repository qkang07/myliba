package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

type cloudPeerStatus struct {
	State     string    `json:"state"`
	Path      string    `json:"path,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitempty"`
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
		if peer.ID == c.id || peer.Endpoint == "" {
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
			status := cloudPeerStatus{State: "unreachable", CheckedAt: time.Now().UTC()}
			if len(peer.LAN) > 0 {
				if grant, err := c.peerGrant(ctx, token); err == nil {
					if resp, err := cloudLANRequest(ctx, peer, http.MethodGet, "/cloud/ping", grant, nil); err == nil {
						resp.Body.Close()
						if resp.StatusCode == http.StatusNoContent {
							status.State, status.Path = "connected", "lan"
						}
					}
				}
			}
			if status.State != "connected" {
				var resp *http.Response
				var err error
				if strings.HasPrefix(peer.Endpoint, "p2p://") {
					resp, err = c.p2pRequest(ctx, http.MethodGet, peer.ID, "/cloud/ping", token, nil)
				} else if len(peer.LAN) == 0 || peer.Endpoint != peer.LAN[0] {
					resp, err = c.request(ctx, http.MethodGet, peer.Endpoint, "/cloud/ping", token, nil)
				}
				if err == nil && resp != nil {
					resp.Body.Close()
					if resp.StatusCode == http.StatusNoContent {
						status.State, status.Path = "connected", "direct"
						if strings.HasPrefix(peer.Endpoint, "p2p://") {
							status.Path = "webrtc"
						}
					}
				}
			}
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
