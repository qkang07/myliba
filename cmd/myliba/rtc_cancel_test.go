package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRTCRequestCancelIsScoped(t *testing.T) {
	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	session := &nodeRTCSession{
		requests: map[string]*rtcRequest{"pending": {}},
		active:   map[string]context.CancelFunc{"first": cancelFirst, "second": cancelSecond},
	}
	manager := &nodeRTCManager{}
	for _, id := range []string{"pending", "first"} {
		payload, err := json.Marshal(wireMessage{Type: "request-cancel", ID: id})
		if err != nil {
			t.Fatal(err)
		}
		manager.handleDataMessage(session, nil, payload)
	}
	if _, exists := session.requests["pending"]; exists {
		t.Fatal("pending request was not removed")
	}
	if first.Err() != context.Canceled {
		t.Fatal("active request was not cancelled")
	}
	if second.Err() != nil {
		t.Fatal("unrelated request was cancelled")
	}
}
