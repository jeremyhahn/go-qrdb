// Copyright 2026 Jeremy Hahn
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

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

func TestNewLessor_DefaultsToTheRealClock(t *testing.T) {
	if NewLessor().clock == nil {
		t.Fatal("a Lessor built without options has no clock")
	}
}

func TestWithClock_NilKeepsTheDefault(t *testing.T) {
	lr := NewLessor(WithClock(nil))
	if lr.clock == nil {
		t.Fatal("WithClock(nil) cleared the default clock")
	}
}

func TestWithClock_InstallsTheClock(t *testing.T) {
	var c clock.Clock = clocktest.New()
	if lr := NewLessor(WithClock(c)); lr.clock != c {
		t.Fatal("WithClock did not install the clock")
	}
}

// TestRemainingTTL_FollowsTheClock verifies the figure a lookup reports is the
// granted TTL minus the time the clock has run, to the second.
func TestRemainingTTL_FollowsTheClock(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	info, err := lr.Grant(nopWriter{}, 60)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if info.RemainingTTL != 60 {
		t.Fatalf("RemainingTTL at grant = %d, want 60", info.RemainingTTL)
	}

	fake.Advance(10 * time.Second)
	if got := lr.Lookup(info.ID).RemainingTTL; got != 50 {
		t.Fatalf("RemainingTTL after 10s = %d, want 50", got)
	}

	fake.Advance(time.Hour)
	if got := lr.Lookup(info.ID).RemainingTTL; got != 0 {
		t.Fatalf("RemainingTTL past expiry = %d, want 0", got)
	}
}

// TestExpiry_IsDecidedAtTheDeadlineNotBeforeOrAfter pins the edge a wall-clock
// test could never hit: the instant the TTL runs out.
func TestExpiry_IsDecidedAtTheDeadlineNotBeforeOrAfter(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	info, err := lr.Grant(nopWriter{}, 1)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}

	fake.Advance(time.Second - time.Nanosecond)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 0 {
		t.Fatalf("lease reported expired a nanosecond early: %v", ids)
	}
	fake.Advance(time.Nanosecond)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 0 {
		t.Fatalf("lease reported expired at its deadline, want strictly after: %v", ids)
	}
	fake.Advance(time.Nanosecond)
	if ids := lr.ExpiredLeaseIDs(); len(ids) != 1 || ids[0] != info.ID {
		t.Fatalf("ExpiredLeaseIDs just past the deadline = %v, want [%d]", ids, info.ID)
	}
}

// TestKeepAlive_RestartsTheTTLFromTheClock verifies a keep-alive pushes the
// deadline a full TTL out from the moment it ran, and that an unrenewed lease
// next to it still expires on its original schedule.
func TestKeepAlive_RestartsTheTTLFromTheClock(t *testing.T) {
	fake := clocktest.New()
	lr := NewLessor(WithClock(fake))
	renewed, _ := lr.Grant(nopWriter{}, 10)
	unrenewed, _ := lr.Grant(nopWriter{}, 10)

	fake.Advance(8 * time.Second)
	if _, err := lr.KeepAlive(nopWriter{}, renewed.ID); err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	fake.Advance(3 * time.Second) // 11s: past the original deadline, 3s into the renewed one

	ids := lr.ExpiredLeaseIDs()
	if len(ids) != 1 || ids[0] != unrenewed.ID {
		t.Fatalf("ExpiredLeaseIDs = %v, want only the unrenewed lease %d", ids, unrenewed.ID)
	}
	if got := lr.Lookup(renewed.ID).RemainingTTL; got != 7 {
		t.Fatalf("renewed lease RemainingTTL = %d, want 7", got)
	}
}

// TestRestore_StartsEveryLeaseAFullTTLFromTheClock verifies a restored lease is
// timed from the restore, not from its original grant.
func TestRestore_StartsEveryLeaseAFullTTLFromTheClock(t *testing.T) {
	fake := clocktest.New()
	st := memory.NewStorage()
	lr := NewLessor(WithClock(fake))
	info, err := lr.Grant(st, 30)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}

	fake.Advance(20 * time.Second)
	if got := lr.Lookup(info.ID).RemainingTTL; got != 10 {
		t.Fatalf("RemainingTTL before restore = %d, want 10", got)
	}
	if err := lr.Restore(st); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := lr.Lookup(info.ID).RemainingTTL; got != 30 {
		t.Fatalf("RemainingTTL after restore = %d, want the full 30", got)
	}
}
