//go:build !integration

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
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock LockService
// ---------------------------------------------------------------------------

type mockLockService struct {
	locks  map[string]*lock.Lock
	lockFn func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
}

func newMockLockService() *mockLockService {
	return &mockLockService{locks: make(map[string]*lock.Lock)}
}

var _ service.LockServicer = (*mockLockService)(nil)

func (m *mockLockService) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holderID, ttl, reentrant)
	}
	l := &lock.Lock{
		Key:          key,
		FencingToken: 1,
		HolderID:     holderID,
		TTL:          int64(ttl.Seconds()),
		Reentrant:    reentrant,
		AcquireCount: 1,
	}
	m.locks[key] = l
	return l, nil
}

func (m *mockLockService) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if l, ok := m.locks[key]; ok {
		if !reentrant || l.HolderID != holderID {
			return nil, errors.New("lock held")
		}
	}
	l := &lock.Lock{
		Key:          key,
		FencingToken: 2,
		HolderID:     holderID,
		TTL:          int64(ttl.Seconds()),
		Reentrant:    reentrant,
		AcquireCount: 1,
	}
	m.locks[key] = l
	return l, nil
}

func (m *mockLockService) Unlock(ctx context.Context, key string, fencingToken int64) error {
	l, ok := m.locks[key]
	if !ok {
		return errors.New("lock not found")
	}
	if l.FencingToken != fencingToken {
		return errors.New("fencing token mismatch")
	}
	delete(m.locks, key)
	return nil
}

func (m *mockLockService) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	l, ok := m.locks[key]
	if !ok {
		return nil, errors.New("lock not found")
	}
	if l.FencingToken != fencingToken {
		return nil, errors.New("fencing token mismatch")
	}
	return l, nil
}

func (m *mockLockService) Status(ctx context.Context, key string) (*lock.Lock, error) {
	l, ok := m.locks[key]
	if !ok {
		return nil, nil // not locked
	}
	return l, nil
}

func (m *mockLockService) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	var result []*lock.Lock
	for k, l := range m.locks {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result = append(result, l)
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func executeLockCmd(t *testing.T, lockSvc service.LockServicer, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(),
		embedded.WithLockService(lockSvc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "table"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newLockCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// newLockAcquireCmd
// ---------------------------------------------------------------------------

func TestLockAcquireCmd_Success(t *testing.T) {
	svc := newMockLockService()
	out, err := executeLockCmd(t, svc, "acquire", "my-lock", "--ttl", "30", "--holder", "node-1")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !strings.Contains(out, "my-lock") {
		t.Errorf("expected lock key in output, got: %q", out)
	}
}

func TestLockAcquireCmd_WithTimeout(t *testing.T) {
	svc := newMockLockService()
	out, err := executeLockCmd(t, svc, "acquire", "timed-lock", "--ttl", "10", "--wait", "5", "--holder", "holder-1")
	if err != nil {
		t.Fatalf("acquire with timeout: %v", err)
	}
	if !strings.Contains(out, "timed-lock") {
		t.Errorf("expected lock key in output, got: %q", out)
	}
}

func TestLockAcquireCmd_ServiceError(t *testing.T) {
	svc := newMockLockService()
	svc.lockFn = func(_ context.Context, _ string, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock service unavailable")
	}
	_, err := executeLockCmd(t, svc, "acquire", "err-lock", "--ttl", "30")
	if err == nil {
		t.Error("expected error when lock service fails")
	}
}

// ---------------------------------------------------------------------------
// newLockTryLockCmd
// ---------------------------------------------------------------------------

func TestLockTryLockCmd_Success(t *testing.T) {
	svc := newMockLockService()
	out, err := executeLockCmd(t, svc, "trylock", "try-lock", "--ttl", "30", "--holder", "node-2")
	if err != nil {
		t.Fatalf("trylock: %v", err)
	}
	if !strings.Contains(out, "try-lock") {
		t.Errorf("expected lock key in output, got: %q", out)
	}
}

func TestLockTryLockCmd_AlreadyHeld(t *testing.T) {
	svc := newMockLockService()
	// First acquire the lock.
	if _, err := svc.Lock(context.Background(), "busy-lock", "holder-A", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// TryLock should fail.
	_, err := executeLockCmd(t, svc, "trylock", "busy-lock", "--ttl", "30", "--holder", "holder-B")
	if err == nil {
		t.Error("expected error when lock is held by another")
	}
}

// ---------------------------------------------------------------------------
// newLockReleaseCmd
// ---------------------------------------------------------------------------

func TestLockReleaseCmd_Success(t *testing.T) {
	svc := newMockLockService()
	// Pre-acquire.
	if _, err := svc.Lock(context.Background(), "rel-lock", "holder-X", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := executeLockCmd(t, svc, "release", "rel-lock", "--token", "1")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestLockReleaseCmd_WrongToken(t *testing.T) {
	svc := newMockLockService()
	if _, err := svc.Lock(context.Background(), "tok-lock", "holder-Y", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := executeLockCmd(t, svc, "release", "tok-lock", "--token", "999")
	if err == nil {
		t.Error("expected error when fencing token mismatches")
	}
}

// ---------------------------------------------------------------------------
// newLockExtendCmd
// ---------------------------------------------------------------------------

func TestLockExtendCmd_Success(t *testing.T) {
	svc := newMockLockService()
	if _, err := svc.Lock(context.Background(), "ext-lock", "holder-Z", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out, err := executeLockCmd(t, svc, "extend", "ext-lock", "--token", "1")
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if !strings.Contains(out, "ext-lock") {
		t.Errorf("expected lock key in output, got: %q", out)
	}
}

func TestLockExtendCmd_WrongToken(t *testing.T) {
	svc := newMockLockService()
	if _, err := svc.Lock(context.Background(), "ext2-lock", "holder-Z2", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := executeLockCmd(t, svc, "extend", "ext2-lock", "--token", "9999")
	if err == nil {
		t.Error("expected error for wrong fencing token in extend")
	}
}

// ---------------------------------------------------------------------------
// newLockStatusCmd
// ---------------------------------------------------------------------------

func TestLockStatusCmd_NotLocked(t *testing.T) {
	svc := newMockLockService()
	out, err := executeLockCmd(t, svc, "status", "nonexistent-lock")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "not locked") {
		t.Errorf("expected 'not locked' in output, got: %q", out)
	}
}

func TestLockStatusCmd_Locked(t *testing.T) {
	svc := newMockLockService()
	if _, err := svc.Lock(context.Background(), "status-lock", "holder-S", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out, err := executeLockCmd(t, svc, "status", "status-lock")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "status-lock") {
		t.Errorf("expected lock key in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// newLockListCmd
// ---------------------------------------------------------------------------

func TestLockListCmd_Empty(t *testing.T) {
	svc := newMockLockService()
	_, err := executeLockCmd(t, svc, "list")
	if err != nil {
		t.Fatalf("list (empty): %v", err)
	}
}

func TestLockListCmd_WithLocks(t *testing.T) {
	svc := newMockLockService()
	if _, err := svc.Lock(context.Background(), "list-lock-1", "holder-L1", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := svc.Lock(context.Background(), "list-lock-2", "holder-L2", 60*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out, err := executeLockCmd(t, svc, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "list-lock-1") && !strings.Contains(out, "list-lock-2") {
		t.Errorf("expected lock keys in output, got: %q", out)
	}
}

func TestLockListCmd_JSON(t *testing.T) {
	svc := newMockLockService()
	if _, err := svc.Lock(context.Background(), "json-lock", "holder-J", 10*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithLockService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "json"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newLockCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"list"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("list(json): %v", err)
	}
}

func TestLockListCmd_YAML(t *testing.T) {
	svc := newMockLockService()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithLockService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = "yaml"
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newLockCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"list"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("list(yaml): %v", err)
	}
}

func TestLockListCmd_WithPrefix(t *testing.T) {
	svc := newMockLockService()
	if _, err := svc.Lock(context.Background(), "app:lock-1", "h1", 30*time.Second, false); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out, err := executeLockCmd(t, svc, "list", "--prefix", "app:")
	if err != nil {
		t.Fatalf("list with prefix: %v", err)
	}
	if !strings.Contains(out, "app:lock-1") {
		t.Errorf("expected prefixed lock in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// newLockAcquireCmd — reentrant flag
// ---------------------------------------------------------------------------

func TestLockAcquireCmd_Reentrant(t *testing.T) {
	svc := newMockLockService()
	out, err := executeLockCmd(t, svc, "acquire", "reentrant-lock", "--ttl", "30", "--reentrant", "--holder", "same-holder")
	if err != nil {
		t.Fatalf("acquire reentrant: %v", err)
	}
	if !strings.Contains(out, "reentrant-lock") {
		t.Errorf("expected lock key in output, got: %q", out)
	}
}
