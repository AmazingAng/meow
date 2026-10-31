package main

import (
	"strings"
	"testing"
)

func TestGatewayConcurrencyConfiguration(t *testing.T) {
	for _, tt := range []struct {
		input   string
		want    int
		invalid bool
	}{
		{"", 0, false}, {"1", 1, false}, {"64", 64, false}, {" 8 ", 8, false},
		{"0", 0, false}, {"-1", 0, true}, {"1.5", 0, true}, {"bad", 0, true},
		{"999999999999999999999999999", 0, true},
	} {
		t.Run(tt.input, func(t *testing.T) {
			t.Setenv("MEOW_GATEWAY_MAX_CONCURRENT_PUSHES", tt.input)
			got, err := gatewayConcurrencyFromEnv()
			if tt.invalid {
				if err == nil || !strings.Contains(err.Error(), "MEOW_GATEWAY_MAX_CONCURRENT_PUSHES") {
					t.Fatalf("invalid setting accepted: %d %v", got, err)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("got %d %v want %d", got, err, tt.want)
			}
		})
	}
}

func TestGatewayInvalidConcurrencyFailsBeforeStartup(t *testing.T) {
	t.Setenv("MEOW_GATEWAY_MAX_CONCURRENT_PUSHES", "-1")
	t.Setenv("MEOW_APNS_KEY_FILE", "")
	err := run(true)
	if err == nil || !strings.Contains(err.Error(), "MEOW_GATEWAY_MAX_CONCURRENT_PUSHES") {
		t.Fatalf("invalid concurrency was not checked before startup: %v", err)
	}
}
