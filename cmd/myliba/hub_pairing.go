package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// pairDesktopHub sends the owner password to the configured HTTPS Hub once.
// The saved profile contains only the device credential returned by the Hub.
func pairDesktopHub(hubURL, deviceID, name, password string) (string, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 15 * time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return pairDesktopHubWithClient(client, hubURL, deviceID, name, password)
}

func pairDesktopHubWithClient(client *http.Client, hubURL, deviceID, name, password string) (string, error) {
	parsed, err := url.Parse(hubURL)
	if err != nil || validateHubURL(hubURL) != nil {
		return "", errors.New("Hub 地址无效")
	}
	origin := "https://" + parsed.Host
	post := func(path string, body any, result any) error {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		request, err := http.NewRequest(http.MethodPost, origin+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("Hub 授权失败（HTTP %d）", response.StatusCode)
		}
		if result == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			return nil
		}
		return json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(result)
	}
	if err := post("/api/session", map[string]string{"password": password}, nil); err != nil {
		return "", err
	}
	defer func() {
		request, err := http.NewRequest(http.MethodDelete, origin+"/api/session", nil)
		if err == nil {
			request.Header.Set("Origin", origin)
			if response, err := client.Do(request); err == nil {
				response.Body.Close()
			}
		}
	}()
	var paired struct {
		Token string `json:"token"`
	}
	if err := post("/api/hub/devices", map[string]any{"id": deviceID, "name": name, "kind": "desktop", "canBrowse": true, "canServe": true, "canCloud": true}, &paired); err != nil {
		return "", err
	}
	if len(paired.Token) < 32 || strings.ContainsAny(paired.Token, "\r\n") {
		return "", errors.New("Hub 未返回有效设备凭据")
	}
	return paired.Token, nil
}
