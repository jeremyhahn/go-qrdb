// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import "testing"

func TestResolvedMaxMessageBytes(t *testing.T) {
	tests := []struct {
		name string
		cfg  TransportConfig
		want int
	}{
		{"unset resolves to default", TransportConfig{}, DefaultTransportMaxMessageBytes},
		{"zero resolves to default", TransportConfig{MaxMessageBytes: 0}, DefaultTransportMaxMessageBytes},
		{"explicit value passes through", TransportConfig{MaxMessageBytes: 1 << 20}, 1 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolvedMaxMessageBytes(); got != tt.want {
				t.Errorf("ResolvedMaxMessageBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolvedMaxConnsPerPeer(t *testing.T) {
	tests := []struct {
		name string
		cfg  TransportConfig
		want int
	}{
		{"unset resolves to default", TransportConfig{}, DefaultTransportMaxConnsPerPeer},
		{"explicit value passes through", TransportConfig{MaxConnsPerPeer: 10}, 10},
		{"negative is an explicit unlimited opt-out", TransportConfig{MaxConnsPerPeer: -1}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolvedMaxConnsPerPeer(); got != tt.want {
				t.Errorf("ResolvedMaxConnsPerPeer() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolvedMaxStreamsPerConn(t *testing.T) {
	tests := []struct {
		name string
		cfg  TransportConfig
		want int
	}{
		{"unset resolves to default", TransportConfig{}, DefaultTransportMaxStreamsPerConn},
		{"explicit value passes through", TransportConfig{MaxStreamsPerConn: 5}, 5},
		{"negative is an explicit unlimited opt-out", TransportConfig{MaxStreamsPerConn: -1}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolvedMaxStreamsPerConn(); got != tt.want {
				t.Errorf("ResolvedMaxStreamsPerConn() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidateTransportMaxMessageBytesNegative(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Transport.MaxMessageBytes = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected a validation error for a negative transport.max_message_bytes")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("error = %T, want *ValidationError", err)
	}
	if ve.Field != "transport.max_message_bytes" {
		t.Errorf("Field = %q, want %q", ve.Field, "transport.max_message_bytes")
	}
}

func TestValidateTransportMaxMessageBytesZeroIsValid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Transport.MaxMessageBytes = 0
	if err := cfg.Validate(); err != nil {
		t.Errorf("zero transport.max_message_bytes should be valid (resolves to the default), got %v", err)
	}
}

func TestDefaultConfigTransportDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Transport.MaxMessageBytes != DefaultTransportMaxMessageBytes {
		t.Errorf("MaxMessageBytes = %d, want %d", cfg.Transport.MaxMessageBytes, DefaultTransportMaxMessageBytes)
	}
	if cfg.Transport.MaxConnsPerPeer != DefaultTransportMaxConnsPerPeer {
		t.Errorf("MaxConnsPerPeer = %d, want %d", cfg.Transport.MaxConnsPerPeer, DefaultTransportMaxConnsPerPeer)
	}
	if cfg.Transport.MaxStreamsPerConn != DefaultTransportMaxStreamsPerConn {
		t.Errorf("MaxStreamsPerConn = %d, want %d", cfg.Transport.MaxStreamsPerConn, DefaultTransportMaxStreamsPerConn)
	}
}
