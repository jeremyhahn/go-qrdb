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
	"os"
	"testing"
)

// openFDCount counts this process's open file descriptors via /proc, the
// same mechanism cmd/qrdbd/cert_reload_lifetime_test.go's
// openInotifyInstances uses for the identical reason: a per-call resource
// leak is a process-wide property, and /proc is the cheap way to observe
// it without instrumenting the code under test.
func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("/proc unavailable, cannot count file descriptors: %v", err)
	}
	return len(entries)
}

// TestBackupService_SoftwareProviderIsCachedNotReopenedPerCall proves the
// default "software" key-wrap provider -- and the *os.Root
// NewSoftwareProvider opens on the configured key directory -- is
// constructed ONCE per BackupService and reused across every
// CreateBackup/RestoreBackup call that resolves it, not freshly
// constructed (and left open, since nothing ever closed it) on every
// single call. Before resolveSoftwareProvider's cache, a probe recorded
// this process's fd count under /proc/self/fd growing by exactly one per
// backup across 200 backups with the same service.
func TestBackupService_SoftwareProviderIsCachedNotReopenedPerCall(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	keyRef := writeTestKeyFile(t, 0x50)
	ctx := context.Background()

	// Warm up: the first call is the one that constructs (and would, on
	// every call under the defect, re-construct) the cached provider.
	if _, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/b", KeyRef: keyRef}); err != nil {
		t.Fatalf("CreateBackup (warm-up): %v", err)
	}

	before := openFDCount(t)
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/b", KeyRef: keyRef}); err != nil {
			t.Fatalf("CreateBackup %d: %v", i, err)
		}
	}
	after := openFDCount(t)

	if after-before >= n {
		t.Fatalf("fd count grew by %d over %d backups (before=%d, after=%d): "+
			"the \"software\" provider's *os.Root is being reopened (and leaked) per call",
			after-before, n, before, after)
	}
}

// TestBackupService_Close_ReleasesCachedSoftwareProvider proves Close
// releases the cached provider's *os.Root: resolving "software" again
// after Close succeeds (a fresh provider is constructed), and Close
// itself is a safe no-op both before any resolution and when called
// twice.
func TestBackupService_Close_ReleasesCachedSoftwareProvider(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	bs := svc.(*BackupService)

	// Close before anything ever resolved "software": a no-op, not a
	// nil-pointer panic.
	if err := bs.Close(); err != nil {
		t.Fatalf("Close before any resolution: %v", err)
	}

	keyRef := writeTestKeyFile(t, 0x51)
	ctx := context.Background()
	if _, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/b", KeyRef: keyRef}); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	bs.softwareProviderMu.Lock()
	cached := bs.softwareProvider
	bs.softwareProviderMu.Unlock()
	if cached == nil {
		t.Fatal("expected a cached software provider after CreateBackup resolved it")
	}

	if err := bs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	bs.softwareProviderMu.Lock()
	cleared := bs.softwareProvider
	bs.softwareProviderMu.Unlock()
	if cleared != nil {
		t.Fatal("Close did not clear the cached software provider")
	}

	// Safe to call twice.
	if err := bs.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// A fresh backup after Close still works: resolveSoftwareProvider
	// constructs a new instance rather than reusing the closed *os.Root.
	if _, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/b", KeyRef: keyRef}); err != nil {
		t.Fatalf("CreateBackup after Close: %v", err)
	}
}
