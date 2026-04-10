//go:build qrdb_vector && gpu

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package gpu

import (
	"errors"
	"testing"
)

func TestMemoryPool_AllocateAndRelease(t *testing.T) {
	pool, err := NewMemoryPool(1024)
	if err != nil {
		t.Fatalf("NewMemoryPool: %v", err)
	}
	defer pool.Close()

	if err := pool.Allocate(256); err != nil {
		t.Fatalf("Allocate 256: %v", err)
	}
	if got := pool.Allocated(); got != 256 {
		t.Errorf("Allocated() = %d, want 256", got)
	}
	if got := pool.Available(); got != 768 {
		t.Errorf("Available() = %d, want 768", got)
	}

	pool.Release(256)
	if got := pool.Allocated(); got != 0 {
		t.Errorf("after release, Allocated() = %d, want 0", got)
	}
}

func TestMemoryPool_Allocate_ExceedsBudget(t *testing.T) {
	pool, err := NewMemoryPool(512)
	if err != nil {
		t.Fatalf("NewMemoryPool: %v", err)
	}
	defer pool.Close()

	if err := pool.Allocate(256); err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	err = pool.Allocate(512)
	if err == nil {
		t.Fatal("expected error when exceeding budget, got nil")
	}
	var memErr *GPUMemoryError
	if !errors.As(err, &memErr) {
		t.Errorf("expected *GPUMemoryError, got %T: %v", err, err)
	}
}

func TestMemoryPool_Release_ClampToZero(t *testing.T) {
	pool, err := NewMemoryPool(1024)
	if err != nil {
		t.Fatalf("NewMemoryPool: %v", err)
	}
	defer pool.Close()

	// Release more than allocated should clamp to zero, not go negative.
	pool.Release(999)
	if got := pool.Allocated(); got != 0 {
		t.Errorf("expected 0 after over-release, got %d", got)
	}
}

func TestMemoryPool_Unbounded_NoLimit(t *testing.T) {
	pool, err := NewMemoryPool(0)
	if err != nil {
		t.Fatalf("NewMemoryPool(0): %v", err)
	}
	defer pool.Close()

	// Unlimited pool should never return a budget error.
	if err := pool.Allocate(1 << 30); err != nil {
		t.Errorf("unbounded pool rejected large allocation: %v", err)
	}
}

func TestMemoryPool_Close_Idempotent(t *testing.T) {
	pool, err := NewMemoryPool(1024)
	if err != nil {
		t.Fatalf("NewMemoryPool: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
}
