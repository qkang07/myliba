package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestCloudLANPinAndGrant(t *testing.T) {
	certificate, key, err := cloudLANCertificate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var listener net.Listener
	for _, address := range cloudLANAddresses("0") {
		u, _ := url.Parse(address)
		listener, err = net.Listen("tcp", u.Host)
		if err == nil {
			break
		}
	}
	if listener == nil {
		t.Skip("no bindable private LAN address")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cloud/ping" || r.Header.Get("Authorization") != "Bearer one-time-grant" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Listener.Close()
	server.Listener = listener
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	node := cloudNode{LAN: []string{server.URL}, LANKey: key}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := cloudLANRequest(ctx, node, http.MethodGet, "/cloud/ping", "one-time-grant", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("ping status = %d", response.StatusCode)
	}
	node.LANKey = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := cloudLANRequest(ctx, node, http.MethodGet, "/cloud/ping", "one-time-grant", nil); err == nil {
		t.Fatal("accepted a mismatched LAN certificate")
	}
}

func TestCloudLANRejectsPublicOrUnpinnedAddress(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000000"
	for _, node := range []cloudNode{
		{LAN: []string{"https://8.8.8.8:4319"}, LANKey: key},
		{LAN: []string{"http://192.168.1.4:4319"}, LANKey: key},
		{LAN: []string{"https://192.168.1.4:4319"}},
		{LAN: []string{"https://example.com:4319"}, LANKey: key},
	} {
		if validCloudLAN(node.LAN, node.LANKey) {
			t.Fatalf("accepted invalid LAN address: %+v", node)
		}
	}
}
