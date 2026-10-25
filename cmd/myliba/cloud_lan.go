package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LAN endpoints are short lived IP addresses. The pinned certificate identity is
// persistent, and authorization still uses a one-time Hub-issued peer grant.
func validCloudLAN(addresses []string, key string) bool {
	if len(addresses) == 0 {
		return key == ""
	}
	if len(addresses) > 8 || len(key) != 64 {
		return false
	}
	if _, err := hex.DecodeString(key); err != nil || strings.ToLower(key) != key {
		return false
	}
	for _, address := range addresses {
		u, err := url.Parse(address)
		if err != nil || u.Scheme != "https" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return false
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return false
		}
		ip, err := netip.ParseAddr(host)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || !ip.IsPrivate() || portNumber < 1 || portNumber > 65535 {
			return false
		}
	}
	return true
}

func cloudLANCertificate(dir string) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, "lan-cert.pem"), filepath.Join(dir, "lan-key.pem")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		private, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return tls.Certificate{}, "", e
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Myliba cloud LAN node"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(20, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, e := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		key, e := x509.MarshalECPrivateKey(private)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: key}), 0600); e != nil {
			return tls.Certificate{}, "", e
		}
		if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
			return tls.Certificate{}, "", e
		}
		cert, err = tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, "", err
		}
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, "", errors.New("empty LAN certificate")
	}
	fingerprint := sha256.Sum256(cert.Certificate[0])
	return cert, hex.EncodeToString(fingerprint[:]), nil
}

func cloudLANAddresses(port string) []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	addresses := make([]string, 0, 4)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		list, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, item := range list {
			prefix, err := netip.ParsePrefix(item.String())
			if err != nil || !prefix.Addr().IsPrivate() {
				continue
			}
			addresses = append(addresses, "https://"+net.JoinHostPort(prefix.Addr().String(), port))
			if len(addresses) == 8 {
				return addresses
			}
		}
	}
	return addresses
}

func (c *cloudRuntime) startLAN(ctx context.Context) {
	if c.lanKey == "" || os.Getenv("MYLIBA_CLOUD_LAN_DISABLED") == "1" {
		return
	}
	address := strings.TrimSpace(os.Getenv("MYLIBA_CLOUD_LAN_LISTEN"))
	if address == "" {
		address = ":0"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Printf("Cloud LAN listener unavailable: %v", err)
		return
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	addresses := cloudLANAddresses(port)
	c.mu.Lock()
	c.lan = addresses
	c.lanPort = port
	c.mu.Unlock()
	server := &http.Server{Handler: c.directHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() {
		if err := server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{c.lanCert}, MinVersion: tls.VersionTLS12})); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Cloud LAN listener failed: %v", err)
		}
		c.mu.Lock()
		c.lan = nil
		c.lanPort = ""
		c.mu.Unlock()
	}()
}

func cloudLANRequest(ctx context.Context, node cloudNode, method, path, grant string, body []byte) (*http.Response, error) {
	if !validCloudLAN(node.LAN, node.LANKey) || len(node.LAN) == 0 {
		return nil, errors.New("no verified LAN address")
	}
	expected, _ := hex.DecodeString(node.LANKey)
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 10 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("LAN peer certificate missing")
		}
		actual := sha256.Sum256(raw[0])
		if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
			return errors.New("LAN peer certificate mismatch")
		}
		return nil
	}}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var last error
	for _, address := range node.LAN {
		req, err := http.NewRequestWithContext(ctx, method, address+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+grant)
		resp, err := client.Do(req)
		if err == nil {
			return resp, nil
		}
		last = err
	}
	return nil, fmt.Errorf("LAN peer unreachable: %w", last)
}

func readCloudRequestBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(body, cloudMaxBlobSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > cloudMaxBlobSize {
		return nil, errors.New("cloud request too large")
	}
	return data, nil
}
