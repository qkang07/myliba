package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Hub auth state is private to the Hub. Only hashes of sessions and device
// tokens are persisted. A password mismatch or damaged file stops startup.
type hubAuthStore struct {
	path string
	data hubAuthData
}

type hubAuthData struct {
	PasswordHash      []byte                    `json:"passwordHash"`
	Sessions          map[string]hubAuthSession `json:"sessions"`
	Devices           map[string]hubAuthDevice  `json:"devices"`
	LegacyNodeAllowed bool                      `json:"legacyNodeAllowed"`
}

type hubAuthSession struct {
	DeviceID     string    `json:"deviceId,omitempty"`
	SelectedNode string    `json:"selectedNode,omitempty"`
	Expires      time.Time `json:"expires"`
}

type hubAuthDevice struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	TokenHash string    `json:"tokenHash"`
	CanBrowse bool      `json:"canBrowse"`
	CanServe  bool      `json:"canServe"`
	CanCloud  bool      `json:"canCloud"`
	CreatedAt time.Time `json:"createdAt"`
	RevokedAt time.Time `json:"revokedAt,omitempty"`
}

func hubAuthKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newHubAuthStore(password string) (*hubAuthStore, error) {
	if len(password) < 16 {
		return nil, errors.New("MYLIBA_WEB_PASSWORD must be at least 16 characters")
	}
	dir := strings.TrimSpace(os.Getenv("MYLIBA_HUB_DATA_DIR"))
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(base, "myliba-hub")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	store := &hubAuthStore{path: filepath.Join(dir, "auth.json")}
	if raw, err := os.ReadFile(store.path); err == nil {
		if err := json.Unmarshal(raw, &store.data); err != nil {
			return nil, fmt.Errorf("Hub auth data is damaged: %w", err)
		}
		if len(store.data.PasswordHash) == 0 || bcrypt.CompareHashAndPassword(store.data.PasswordHash, []byte(password)) != nil {
			return nil, errors.New("MYLIBA_WEB_PASSWORD does not match the existing Hub owner password")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		store.data.PasswordHash = passwordHash
		store.data.LegacyNodeAllowed = len(strings.TrimSpace(os.Getenv("MYLIBA_NODE_TOKEN"))) >= 32
	}
	if store.data.Sessions == nil {
		store.data.Sessions = map[string]hubAuthSession{}
	}
	if store.data.Devices == nil {
		store.data.Devices = map[string]hubAuthDevice{}
	}
	for key, session := range store.data.Sessions {
		if time.Now().After(session.Expires) {
			delete(store.data.Sessions, key)
		}
	}
	if err := store.save(); err != nil {
		return nil, err
	}
	return store, nil
}

// Caller serializes writes with remoteHub.mu.
func (s *hubAuthStore) save() error {
	return s.persist(s.data)
}

func (s *hubAuthStore) update(change func(*hubAuthData) error) error {
	next := hubAuthData{PasswordHash: s.data.PasswordHash, Sessions: map[string]hubAuthSession{}, Devices: map[string]hubAuthDevice{}, LegacyNodeAllowed: s.data.LegacyNodeAllowed}
	for key, value := range s.data.Sessions {
		next.Sessions[key] = value
	}
	for key, value := range s.data.Devices {
		next.Devices[key] = value
	}
	if err := change(&next); err != nil {
		return err
	}
	if err := s.persist(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *hubAuthStore) persist(data hubAuthData) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
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
	return os.Rename(file.Name(), s.path)
}
