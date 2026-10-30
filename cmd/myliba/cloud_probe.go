package main

import (
	"context"
	"fmt"
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
	State          string `json:"state"`
	RTTMillis      int64  `json:"rttMillis,omitempty"`
	ConnectMillis  int64  `json:"connectMillis,omitempty"`
	SpeedBps       int64  `json:"speedBps,omitempty"`
	Bytes          int64  `json:"bytes,omitempty"`
	DurationMillis int64  `json:"durationMillis,omitempty"`
	HTTPStatus     int    `json:"httpStatus,omitempty"`
	SelectedPair   string `json:"selectedPair,omitempty"`
	Error          string `json:"error,omitempty"`
}

type cloudSpeedTestResult struct {
	PeerID         string             `json:"peerId"`
	Path           string             `json:"path"`
	State          string             `json:"state"`
	Bytes          int64              `json:"bytes,omitempty"`
	DurationMillis int64              `json:"durationMillis,omitempty"`
	SpeedBps       int64              `json:"speedBps,omitempty"`
	ConnectMillis  int64              `json:"connectMillis,omitempty"`
	SelectedPair   string             `json:"selectedPair,omitempty"`
	Error          string             `json:"error,omitempty"`
	StartedAt      time.Time          `json:"startedAt"`
	Rounds         int                `json:"rounds,omitempty"`
	Samples        []cloudSpeedSample `json:"samples,omitempty"`
}

type cloudSpeedSample struct {
	Bytes          int64 `json:"bytes"`
	DurationMillis int64 `json:"durationMillis"`
	ConnectMillis  int64 `json:"connectMillis"`
	SpeedBps       int64 `json:"speedBps"`
}

const cloudSpeedTestBytes = 4 << 20

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
			return cloudPathCheck{State: "unreachable", Error: cloudDiagnosticError(err)}
		}
		count, copyErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		elapsed := time.Since(started)
		check := cloudPathCheck{State: "connected", ConnectMillis: elapsed.Milliseconds(), Bytes: count, DurationMillis: elapsed.Milliseconds(), HTTPStatus: resp.StatusCode}
		check.SelectedPair = resp.Header.Get("X-Myliba-Selected-ICE-Pair")
		if resp.StatusCode != http.StatusOK || copyErr != nil || count == 0 {
			check.State = "unreachable"
			check.Error = cloudDiagnosticError(copyErr)
			if resp.StatusCode != http.StatusOK {
				check.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
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
			return cloudPathCheck{State: "unreachable", Error: cloudDiagnosticError(err)}
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return cloudPathCheck{State: "unreachable", Error: fmt.Sprintf("ping HTTP %d", resp.StatusCode)}
		}
		total += time.Since(started)
	}
	check := cloudPathCheck{State: "connected", RTTMillis: total.Milliseconds() / 3}
	started := time.Now()
	resp, err := request("/cloud/diagnostic-payload?bytes=262144")
	if err == nil && resp != nil {
		check.HTTPStatus = resp.StatusCode
		count, copyErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		elapsed := time.Since(started)
		if copyErr == nil && count > 0 && elapsed > 0 {
			check.SpeedBps = int64(float64(count) / elapsed.Seconds())
			check.Bytes = count
			check.DurationMillis = elapsed.Milliseconds()
		} else if copyErr != nil {
			check.Error = cloudDiagnosticError(copyErr)
		} else if resp.StatusCode != http.StatusOK {
			check.Error = fmt.Sprintf("diagnostic HTTP %d", resp.StatusCode)
		}
	} else {
		check.Error = cloudDiagnosticError(err)
	}
	return check
}

func cloudDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}
