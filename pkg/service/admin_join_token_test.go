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

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func TestAdminService_MintJoinToken_NotConfigured(t *testing.T) {
	svc := NewAdminService(&mockHost{})
	_, _, err := svc.(*AdminService).MintJoinToken(context.Background(), "node-1", time.Hour)
	var notConfigured *ClusterJoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("MintJoinToken with no store configured = %v, want *ClusterJoinTokenNotConfiguredError", err)
	}
}

func TestAdminService_MintJoinToken_WithStateOption(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	admin := svc.(*AdminService)

	token, expiresAt, err := admin.MintJoinToken(context.Background(), "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	if token == "" {
		t.Fatal("MintJoinToken returned an empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %v, want a future time", expiresAt)
	}

	// The token is single-use: a second presentation is refused.
	boundURI, err := clusterenroll.ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	if err != nil {
		t.Fatalf("ConsumeJoinToken (first use): %v", err)
	}
	if boundURI == "" {
		t.Fatal("ConsumeJoinToken returned an empty bound identity")
	}
	if _, err := clusterenroll.ConsumeJoinToken(context.Background(), store, "test.cluster", token); err == nil {
		t.Fatal("ConsumeJoinToken (second use) succeeded, want refusal")
	}
}

func TestAdminService_MintJoinToken_SetAfterConstruction(t *testing.T) {
	// Mirrors cmd/qrdbd's actual call pattern: AdminService is constructed
	// before the replicated KV store exists, and SetClusterJoinTokenStore
	// binds the store once it does.
	svc := NewAdminService(&mockHost{})
	admin := svc.(*AdminService)
	if _, _, err := admin.MintJoinToken(context.Background(), "node-1", time.Hour); err == nil {
		t.Fatal("MintJoinToken before SetClusterJoinTokenStore succeeded, want *ClusterJoinTokenNotConfiguredError")
	}

	store := kvstore.NewMemoryKVStore()
	admin.SetClusterJoinTokenStore(store, "test.cluster")
	if _, _, err := admin.MintJoinToken(context.Background(), "node-1", time.Hour); err != nil {
		t.Fatalf("MintJoinToken after SetClusterJoinTokenStore: %v", err)
	}
}

func TestAdminService_MintJoinToken_InvalidTTLSurfacesTypedError(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	admin := svc.(*AdminService)

	_, _, err := admin.MintJoinToken(context.Background(), "node-1", time.Millisecond)
	var ttlErr *clusterenroll.JoinTokenTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("MintJoinToken with a too-short TTL = %v, want *clusterenroll.JoinTokenTTLError", err)
	}
}

// TestParseJoinTokenTTLSeconds covers every transport's conversion of a
// wire-supplied ttl_seconds int64 to a time.Duration: the ordinary range,
// the zero/negative "use the default" convention, exactly at the overflow
// boundary, and just past it.
func TestParseJoinTokenTTLSeconds(t *testing.T) {
	cases := []struct {
		name       string
		ttlSeconds int64
		want       time.Duration
		wantErr    bool
	}{
		{name: "ordinary value", ttlSeconds: 3600, want: time.Hour},
		{name: "zero means use the default", ttlSeconds: 0, want: 0},
		{name: "negative means use the default", ttlSeconds: -1, want: 0},
		{name: "exactly at the overflow boundary", ttlSeconds: MaxJoinTokenTTLSeconds, want: time.Duration(MaxJoinTokenTTLSeconds) * time.Second},
		{name: "one second past the boundary overflows", ttlSeconds: MaxJoinTokenTTLSeconds + 1, wantErr: true},
		{name: "math.MaxInt64 overflows", ttlSeconds: math.MaxInt64, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJoinTokenTTLSeconds(tc.ttlSeconds)
			if tc.wantErr {
				var overflow *JoinTokenTTLOverflowError
				if !errors.As(err, &overflow) {
					t.Fatalf("ParseJoinTokenTTLSeconds(%d) = %v, want *JoinTokenTTLOverflowError", tc.ttlSeconds, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseJoinTokenTTLSeconds(%d): %v", tc.ttlSeconds, err)
			}
			if got != tc.want {
				t.Fatalf("ParseJoinTokenTTLSeconds(%d) = %v, want %v", tc.ttlSeconds, got, tc.want)
			}
		})
	}
}

// TestParseJoinTokenTTLSeconds_NeverWrapsNegative proves the specific
// failure mode this guards against cannot happen: every ttlSeconds this
// function accepts (does not refuse) converts to a NON-NEGATIVE duration,
// never a value that silently wrapped around int64.
func TestParseJoinTokenTTLSeconds_NeverWrapsNegative(t *testing.T) {
	for _, ttlSeconds := range []int64{0, 1, time.Hour.Nanoseconds() / int64(time.Second), MaxJoinTokenTTLSeconds} {
		got, err := ParseJoinTokenTTLSeconds(ttlSeconds)
		if err != nil {
			t.Fatalf("ParseJoinTokenTTLSeconds(%d): %v", ttlSeconds, err)
		}
		if got < 0 {
			t.Fatalf("ParseJoinTokenTTLSeconds(%d) = %v, a wrapped negative duration", ttlSeconds, got)
		}
	}
}
