package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (c *cloudClient) speedTest(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "测速路径无效")
		return
	}
	path := strings.TrimSpace(input.Path)
	if path != "lan" && path != "webrtc" && path != "https" && path != "relay" {
		writeError(w, http.StatusBadRequest, "不支持的测速路径")
		return
	}
	peerID := r.PathValue("id")
	if !validCloudNodeID(peerID) || peerID == c.runtime.id {
		writeError(w, http.StatusBadRequest, "节点 ID 无效")
		return
	}
	var peer *cloudNode
	for _, candidate := range c.runtime.activePeers() {
		if candidate.ID == peerID {
			copy := candidate
			peer = &copy
			break
		}
	}
	if peer == nil {
		writeError(w, http.StatusNotFound, "该节点当前不在线")
		return
	}
	result := c.runtime.speedTest(r.Context(), *peer, path)
	writeJSON(w, http.StatusOK, result)
}

func (c *cloudClient) diagnosePeer(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	peerID := r.PathValue("id")
	if !validCloudNodeID(peerID) || peerID == c.runtime.id {
		writeError(w, http.StatusBadRequest, "节点 ID 无效")
		return
	}
	var peer *cloudNode
	for _, candidate := range c.runtime.activePeers() {
		if candidate.ID == peerID {
			copy := candidate
			peer = &copy
			break
		}
	}
	if peer == nil {
		writeError(w, http.StatusNotFound, "该节点当前不在线")
		return
	}
	_, token, err := c.runtime.credentials()
	if err != nil || len(token) < 32 {
		writeError(w, http.StatusServiceUnavailable, "云层授权不可用")
		return
	}
	c.runtime.probePeers([]cloudNode{*peer}, token)
	status := c.runtime.peerStatuses()[peer.ID]
	writeJSON(w, http.StatusOK, map[string]any{
		"peer": map[string]any{
			"id": peer.ID, "role": peer.Role, "seenAt": peer.SeenAt,
			"endpointType": cloudEndpointType(*peer), "lanAddresses": len(peer.LAN),
		},
		"diagnostics": status,
	})
}

func cloudEndpointType(peer cloudNode) string {
	switch {
	case strings.HasPrefix(peer.Endpoint, "p2p://"):
		return "webrtc"
	case peer.Relayed:
		return "relay"
	case strings.HasPrefix(peer.Endpoint, "https://"):
		return "https"
	default:
		return "waiting"
	}
}

func (c *cloudRuntime) speedTest(parent context.Context, peer cloudNode, path string) cloudSpeedTestResult {
	result := cloudSpeedTestResult{PeerID: peer.ID, Path: path, State: "failed", StartedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	_, token, err := c.credentials()
	if err != nil || len(token) < 32 {
		result.Error = "云层授权不可用"
		return result
	}
	request := func() (*http.Response, error) {
		route := fmt.Sprintf("/cloud/diagnostic-payload?bytes=%d", cloudSpeedTestBytes)
		switch path {
		case "lan":
			grant, grantErr := c.peerGrant(ctx, token)
			if grantErr != nil {
				return nil, grantErr
			}
			return cloudLANRequest(ctx, peer, http.MethodGet, route, grant, nil)
		case "webrtc":
			if !strings.HasPrefix(peer.Endpoint, "p2p://") {
				return nil, fmt.Errorf("节点没有 WebRTC 地址")
			}
			return c.p2pRequest(ctx, http.MethodGet, peer.ID, route, token, nil)
		case "https", "relay":
			if !strings.HasPrefix(peer.Endpoint, "https://") || (path == "relay") != peer.Relayed {
				return nil, fmt.Errorf("节点没有对应的 HTTPS 路径")
			}
			grant, grantErr := c.peerGrant(ctx, token)
			if grantErr != nil {
				return nil, grantErr
			}
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(peer.Endpoint, "/")+route, nil)
			if requestErr != nil {
				return nil, requestErr
			}
			req.Header.Set("Authorization", "Bearer "+grant)
			return c.client.Do(req)
		default:
			return nil, fmt.Errorf("不支持的测速路径")
		}
	}
	const rounds = 3
	started := time.Now()
	result.Rounds = rounds
	result.Samples = make([]cloudSpeedSample, 0, rounds)
	var totalDuration time.Duration
	var totalConnect time.Duration
	for i := 0; i < rounds; i++ {
		start := time.Now()
		resp, requestErr := request()
		if requestErr != nil {
			result.Error = fmt.Sprintf("第 %d 轮：%s", i+1, cloudDiagnosticError(requestErr))
			result.DurationMillis = time.Since(started).Milliseconds()
			return result
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			result.Error = fmt.Sprintf("第 %d 轮：节点返回 HTTP %d", i+1, resp.StatusCode)
			result.DurationMillis = time.Since(started).Milliseconds()
			return result
		}
		if pair := resp.Header.Get("X-Myliba-Selected-ICE-Pair"); pair != "" {
			result.SelectedPair = pair
		}
		requestElapsed := time.Since(start)
		payloadElapsed := time.Duration(0)
		if raw := resp.Header.Get("X-Myliba-Payload-Duration-Millis"); raw != "" {
			if millis, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && millis > 0 {
				payloadElapsed = time.Duration(millis) * time.Millisecond
			}
		}
		copyStarted := time.Now()
		count, readErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		copyElapsed := time.Since(copyStarted)
		if payloadElapsed == 0 {
			payloadElapsed = copyElapsed
		}
		connectElapsed := requestElapsed - payloadElapsed
		if connectElapsed < 0 {
			connectElapsed = 0
		}
		elapsed := payloadElapsed
		totalDuration += elapsed
		totalConnect += connectElapsed
		if readErr != nil {
			result.Error = fmt.Sprintf("第 %d 轮：%s", i+1, cloudDiagnosticError(readErr))
			result.DurationMillis = time.Since(started).Milliseconds()
			return result
		}
		if count != cloudSpeedTestBytes || elapsed <= 0 {
			result.Error = fmt.Sprintf("第 %d 轮：测速数据不完整", i+1)
			result.DurationMillis = time.Since(started).Milliseconds()
			return result
		}
		result.Bytes += count
		result.Samples = append(result.Samples, cloudSpeedSample{Bytes: count, DurationMillis: elapsed.Milliseconds(), ConnectMillis: connectElapsed.Milliseconds(), SpeedBps: int64(float64(count) / elapsed.Seconds())})
	}
	result.DurationMillis = time.Since(started).Milliseconds()
	if totalDuration <= 0 {
		result.Error = "测速用时无效"
		return result
	}
	result.State = "complete"
	result.SpeedBps = int64(float64(result.Bytes) / totalDuration.Seconds())
	result.ConnectMillis = (totalConnect / rounds).Milliseconds()
	return result
}
