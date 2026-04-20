package main

import (
	"testing"
	"time"
)

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		state   string
		created int64
		want    string
	}{
		{"exited", time.Now().Unix(), "-"},
		{"running", time.Now().Add(-30 * time.Second).Unix(), "30s"},
		{"running", time.Now().Add(-5 * time.Minute).Unix(), "5m"},
		{"running", time.Now().Add(-90 * time.Minute).Unix(), "1h30m"},
		{"running", time.Now().Add(-2 * time.Hour).Unix(), "2h"},
		{"running", time.Now().Add(-25 * time.Hour).Unix(), "1d1h"},
		{"running", time.Now().Add(-48 * time.Hour).Unix(), "2d"},
	}

	for _, tt := range tests {
		got := formatUptime(tt.state, tt.created)
		if got != tt.want {
			t.Errorf("formatUptime(%q, %d) = %q, want %q", tt.state, tt.created, got, tt.want)
		}
	}
}

func TestFormatPorts(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"empty", "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatPorts(nil)
			if got != tt.want {
				t.Errorf("formatPorts(nil) = %q, want %q", got, tt.want)
			}
		})
	}
}
