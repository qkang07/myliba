package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type cloudGrant struct {
	DeviceID string
	Expires  time.Time
}

// A cloud bearer is an individually revocable device credential issued by
// this Hub. Cloud peers ask the Hub to validate each other's credentials.
func (h *remoteHub) cloudDeviceID(token string) (string, bool) {
	if len(token) < 32 || len(token) > 4096 || h.auth == nil {
		return "", false
	}
	key := hubAuthKey(token)
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, device := range h.auth.data.Devices {
		if device.RevokedAt.IsZero() && device.CanCloud && safeEqual(device.TokenHash, key) {
			return id, true
		}
	}
	return "", false
}

func (h *remoteHub) cloudBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := r.Header.Get("Authorization")
		if !strings.HasPrefix(value, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, allowed := h.cloudDeviceID(strings.TrimPrefix(value, "Bearer ")); !allowed {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *remoteHub) verifyCloudPeer(w http.ResponseWriter, r *http.Request) {
	self := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	_, allowed := h.cloudDeviceID(self)
	if !allowed {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if input.Token == self {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.mu.Lock()
	grant, exists := h.cloudGrants[hubAuthKey(input.Token)]
	delete(h.cloudGrants, hubAuthKey(input.Token))
	device := h.auth.data.Devices[grant.DeviceID]
	h.mu.Unlock()
	if !exists || time.Now().After(grant.Expires) || !device.RevokedAt.IsZero() || !device.CanCloud {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *remoteHub) issueCloudGrant(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, allowed := h.cloudDeviceID(strings.TrimPrefix(auth, "Bearer "))
	if !allowed {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token, err := randomToken()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	h.mu.Lock()
	if h.cloudGrants == nil {
		h.cloudGrants = map[string]cloudGrant{}
	}
	for key, grant := range h.cloudGrants {
		if time.Now().After(grant.Expires) {
			delete(h.cloudGrants, key)
		}
	}
	if len(h.cloudGrants) >= 4096 {
		h.mu.Unlock()
		http.Error(w, "too many cloud grants", http.StatusTooManyRequests)
		return
	}
	h.cloudGrants[hubAuthKey(token)] = cloudGrant{DeviceID: id, Expires: time.Now().Add(2 * time.Minute)}
	h.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}
