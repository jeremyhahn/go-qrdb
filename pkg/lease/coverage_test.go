// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package lease

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

// TestRemainingTTL_Expired verifies that remainingTTL returns 0 for an
// already-expired lease rather than a negative value.
func TestRemainingTTL_Expired(t *testing.T) {
	now := clocktest.New().Now()
	l := &lease{
		id:     1,
		ttl:    1,
		expiry: now.Add(-10 * time.Second), // expired in the past
		keys:   make(map[string]struct{}),
	}
	// remainingTTLLocked, not remainingTTL: the caller must hold l.mu, and a
	// test holding the only reference to this lease trivially does (#174).
	got := l.remainingTTLLocked(now)
	if got != 0 {
		t.Errorf("remainingTTLLocked() on expired lease = %d, want 0", got)
	}
}

// TestRemainingTTL_Active verifies that remainingTTL returns a positive
// number for an active lease.
func TestRemainingTTL_Active(t *testing.T) {
	now := clocktest.New().Now()
	l := &lease{
		id:     2,
		ttl:    60,
		expiry: now.Add(60 * time.Second),
		keys:   make(map[string]struct{}),
	}
	got := l.remainingTTLLocked(now)
	if got <= 0 {
		t.Errorf("remainingTTLLocked() on active lease = %d, want > 0", got)
	}
}

// TestLeases_FiltersExpired verifies that Leases does not return expired
// leases, exercising the isExpired branch inside Leases.
func TestLeases_FiltersExpired(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))

	// Grant an active lease.
	active, err := lr.Grant(nopWriter{}, 60)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}

	// Inject an expired lease directly to trigger the isExpired branch.
	expiredID := int64(9999)
	lr.mu.Lock()
	lr.leases[expiredID] = &lease{
		id:     expiredID,
		ttl:    1,
		expiry: fake.Now().Add(-5 * time.Second),
		keys:   make(map[string]struct{}),
	}
	lr.mu.Unlock()

	all := lr.Leases()

	// Only the active lease should be present.
	for _, l := range all {
		if l.ID == expiredID {
			t.Error("Leases() returned an expired lease")
		}
	}

	found := false
	for _, l := range all {
		if l.ID == active.ID {
			found = true
		}
	}
	if !found {
		t.Error("Leases() did not return the active lease")
	}
}

// TestLeases_Empty verifies that Leases returns an empty slice when no
// active leases exist.
func TestLeases_Empty(t *testing.T) {
	lr := NewLessor()
	all := lr.Leases()
	if len(all) != 0 {
		t.Errorf("Leases() on empty lessor = %d, want 0", len(all))
	}
}
