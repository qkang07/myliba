package main

import (
	"context"
	"testing"
	"time"
)

func TestHubRelayRateConfiguration(t *testing.T) {
	t.Setenv("MYLIBA_HUB_RELAY_BYTES_PER_SECOND", "4096")
	if rate, err := hubRelayBytesPerSecond(); err != nil || rate != 4096 {
		t.Fatalf("valid relay rate: %d, %v", rate, err)
	}
	for _, invalid := range []string{"0", "4095", "not-a-number", "1073741825"} {
		t.Setenv("MYLIBA_HUB_RELAY_BYTES_PER_SECOND", invalid)
		if _, err := hubRelayBytesPerSecond(); err == nil {
			t.Fatalf("accepted invalid relay rate %q", invalid)
		}
	}
}

func TestHubRelayLimiterRespectsRateAndCancellation(t *testing.T) {
	limiter := relayRateLimiter{bps: 8192}
	start := time.Now()
	if err := limiter.wait(context.Background(), 4096); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("relay limiter sent content too fast: %v", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.wait(ctx, 4096); err != context.Canceled {
		t.Fatalf("canceled transfer did not stop: %v", err)
	}
}

func TestResourceUDPPortRangeValidation(t *testing.T) {
	t.Setenv("MYLIBA_RESOURCE_UDP_PORT_RANGE", "40000-40031")
	if _, err := newNodeRTCManager(nil, nil, nil); err != nil {
		t.Fatalf("valid UDP port range: %v", err)
	}
	for _, invalid := range []string{"1-2", "40000-40001", "40031-40000", "bad"} {
		t.Setenv("MYLIBA_RESOURCE_UDP_PORT_RANGE", invalid)
		if _, err := newNodeRTCManager(nil, nil, nil); err == nil {
			t.Fatalf("accepted invalid UDP port range %q", invalid)
		}
	}
}
