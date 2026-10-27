//go:build qrdb_embedded

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

package main

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/raftadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Compile-time interface satisfaction checks
// ---------------------------------------------------------------------------

// Verify that *hostAdapter satisfies both halves of a node host, the data
// host (service.Host) and the Raft-admin host (raftadmin.Host), at compile
// time. This ensures that any future additions to either interface that are
// not implemented on hostAdapter will cause a build failure rather than a
// runtime panic.
var (
	_ service.Host   = (*hostAdapter)(nil)
	_ raftadmin.Host = (*hostAdapter)(nil)
)

// Verify that *bootstrapHostAdapter satisfies both halves at compile time.
// bootstrapHostAdapter embeds hostAdapter and overrides SyncRead, so it must
// also satisfy the full contract.
var (
	_ service.Host   = (*bootstrapHostAdapter)(nil)
	_ raftadmin.Host = (*bootstrapHostAdapter)(nil)
)

// ---------------------------------------------------------------------------
// hostAdapter interface satisfaction tests
// ---------------------------------------------------------------------------

func TestHostAdapter_ImplementsServiceHost(t *testing.T) {
	// The compile-time check (var _ service.Host = (*hostAdapter)(nil))
	// above is the real assertion. This test exists to provide a clear
	// test name in the output and to verify the assignment compiles.
	var h service.Host = (*hostAdapter)(nil)
	_ = h
}

func TestBootstrapHostAdapter_ImplementsServiceHost(t *testing.T) {
	// The compile-time check (var _ service.Host = (*bootstrapHostAdapter)(nil))
	// above is the real assertion. This test exists to provide a clear
	// test name in the output and to verify the assignment compiles.
	var h service.Host = (*bootstrapHostAdapter)(nil)
	_ = h
}

// ---------------------------------------------------------------------------
// resolveStorageSM tests
// ---------------------------------------------------------------------------

func TestResolveStorageSM_KnownBackends(t *testing.T) {
	cases := []struct {
		name     string
		backend  string
		expected string
	}{
		{
			name:     "pebble maps to pebble",
			backend:  "pebble",
			expected: "pebble",
		},
		{
			name:     "mvcc maps to mvcc",
			backend:  "mvcc",
			expected: "mvcc",
		},
		{
			name:     "file maps to pebble",
			backend:  "file",
			expected: "pebble",
		},
		{
			name:     "raft maps to pebble",
			backend:  "raft",
			expected: "pebble",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStorageSM(tc.backend)
			if got != tc.expected {
				t.Fatalf("resolveStorageSM(%q): got %q, want %q", tc.backend, got, tc.expected)
			}
		})
	}
}

func TestResolveStorageSM_UnknownBackend(t *testing.T) {
	cases := []struct {
		name    string
		backend string
	}{
		{
			name:    "completely unknown string",
			backend: "rocksdb",
		},
		{
			name:    "numeric string",
			backend: "12345",
		},
		{
			name:    "mixed case is not matched",
			backend: "Pebble",
		},
		{
			name:    "uppercase is not matched",
			backend: "MVCC",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStorageSM(tc.backend)
			if got != "pebble" {
				t.Fatalf("resolveStorageSM(%q): got %q, want %q", tc.backend, got, "pebble")
			}
		})
	}
}

func TestResolveStorageSM_EmptyString(t *testing.T) {
	got := resolveStorageSM("")
	if got != "pebble" {
		t.Fatalf("resolveStorageSM(%q): got %q, want %q", "", got, "pebble")
	}
}

func TestResolveStorageSM_EmptyString_IsConsistentDefault(t *testing.T) {
	// Verify that empty string produces the same result as an explicitly
	// unknown backend, confirming a single default code path.
	empty := resolveStorageSM("")
	unknown := resolveStorageSM("nonexistent-backend")
	if empty != unknown {
		t.Fatalf("expected empty and unknown backends to resolve identically: got %q vs %q", empty, unknown)
	}
}
