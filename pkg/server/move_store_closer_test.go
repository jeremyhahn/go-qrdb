// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeMoveShards struct {
	started map[StartedShard]bool
	calls   []string
	err     error
	// onClose and onAttach, when set, run at the start of Close and Attach.
	onClose  func()
	onAttach func()
}

func (f *fakeMoveShards) Attach(_ context.Context, tenantID, db string) error {
	if f.onAttach != nil {
		f.onAttach()
	}
	f.calls = append(f.calls, "attach "+tenantID+"/"+db)
	f.started[StartedShard{TenantID: tenantID, Database: db}] = true
	return f.err
}

func (f *fakeMoveShards) Close(tenantID, db string) error {
	if f.onClose != nil {
		f.onClose()
	}
	f.calls = append(f.calls, "close "+tenantID+"/"+db)
	delete(f.started, StartedShard{TenantID: tenantID, Database: db})
	return f.err
}

func (f *fakeMoveShards) ListStarted() []StartedShard {
	out := make([]StartedShard, 0, len(f.started))
	for s := range f.started {
		out = append(out, s)
	}
	return out
}

type fakeMoveBarriers struct {
	calls []string
	err   error
}

func (f *fakeMoveBarriers) CloseVolumeTenant(_ context.Context, tenantID string) error {
	f.calls = append(f.calls, "close "+tenantID)
	return f.err
}

func (f *fakeMoveBarriers) ReopenVolumeTenant(_ context.Context, tenantID string) error {
	f.calls = append(f.calls, "reopen "+tenantID)
	return f.err
}

func TestMoveStoreCloser_CloseThenReopenAwaitsCatchUp(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "b"}:  true,
		{TenantID: "acme", Database: "a"}:  true,
		{TenantID: "other", Database: "a"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	reads := 0
	caughtUp := func(context.Context, string, string) (bool, error) {
		reads++
		return reads > 2, nil
	}
	c, err := NewMoveStoreCloser(shards, barriers, caughtUp, time.Millisecond)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !shards.started[StartedShard{TenantID: "other", Database: "a"}] || len(shards.started) != 1 {
		t.Fatalf("started = %v, want only the other tenant's shard", shards.started)
	}
	if err := c.OpenTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if want := []string{"close acme", "reopen acme"}; !reflect.DeepEqual(barriers.calls, want) {
		t.Fatalf("barrier calls = %v, want %v", barriers.calls, want)
	}
	if len(shards.started) != 3 || reads < 3 {
		t.Fatalf("started = %v, catch-up reads = %d", shards.started, reads)
	}
}

// TestMoveStoreCloser_HoldsTenantAcrossMove proves the placement reconciler's
// hold: HoldsTenant is true before the first shard closes (so before the
// barrier closes) and stays true until OpenTenantStores returns.
func TestMoveStoreCloser_HoldsTenantAcrossMove(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	caughtUp := func(context.Context, string, string) (bool, error) { return true, nil }
	c, err := NewMoveStoreCloser(shards, barriers, caughtUp, time.Millisecond)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme held before any move")
	}
	var heldAtClose, otherAtClose, heldAtAttach bool
	shards.onClose = func() {
		heldAtClose = c.HoldsTenant("acme")
		otherAtClose = c.HoldsTenant("other")
	}
	shards.onAttach = func() { heldAtAttach = c.HoldsTenant("acme") }
	ctx := context.Background()
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !heldAtClose || otherAtClose {
		t.Fatalf("at shard close: acme held = %v, other held = %v", heldAtClose, otherAtClose)
	}
	if !c.HoldsTenant("acme") {
		t.Fatal("acme released after close, before reopen")
	}
	if err := c.OpenTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if !heldAtAttach {
		t.Fatal("acme released before its shards were re-attached")
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme still held after OpenTenantStores returned")
	}
}

// TestMoveStoreCloser_FailedStepsReleaseHold: neither a failed close nor a
// failed reopen leaves the tenant held forever.
func TestMoveStoreCloser_FailedStepsReleaseHold(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	c, err := NewMoveStoreCloser(shards, barriers, nil, time.Millisecond)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	shards.err = errors.New("close failed")
	if err := c.CloseTenantStores(ctx, "acme"); err == nil {
		t.Fatal("close: want error")
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme held after a failed close")
	}
	shards.err = nil
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	barriers.err = errors.New("reopen failed")
	if err := c.OpenTenantStores(ctx, "acme"); err == nil {
		t.Fatal("open: want error")
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme held after a failed reopen")
	}
}

func TestMoveStoreCloser_RetryReopenFinishesAFailedReopen(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	catchUpErr := errors.New("catch-up timed out")
	failCatchUp := true
	c, err := NewMoveStoreCloser(shards, barriers, func(context.Context, string, string) (bool, error) {
		if failCatchUp {
			return false, catchUpErr
		}
		return true, nil
	}, time.Millisecond)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := c.RetryReopen(ctx, "acme"); err != nil {
		t.Fatalf("retry of a tenant never closed: %v", err)
	}
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.OpenTenantStores(ctx, "acme"); !errors.Is(err, catchUpErr) {
		t.Fatalf("open = %v, want the catch-up error", err)
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme held after a failed reopen")
	}
	if got := c.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("pending = %v, want [acme]", got)
	}
	// A retry while a move step holds the tenant leaves it to that step.
	c.hold("acme")
	if err := c.RetryReopen(ctx, "acme"); err != nil {
		t.Fatalf("held retry: %v", err)
	}
	c.release("acme")
	failCatchUp = false
	if err := c.RetryReopen(ctx, "acme"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := c.PendingReopen(); len(got) != 0 {
		t.Fatalf("pending after the retry = %v", got)
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme held after the retry")
	}
	// The barrier reopened once, and the shard attached once: the retry
	// does not redo a reopen step that already succeeded.
	if want := []string{"close acme", "reopen acme"}; !reflect.DeepEqual(barriers.calls, want) {
		t.Fatalf("barrier calls = %v, want %v", barriers.calls, want)
	}
	if want := []string{"close acme/a", "attach acme/a"}; !reflect.DeepEqual(shards.calls, want) {
		t.Fatalf("shard calls = %v, want %v", shards.calls, want)
	}
}

func TestMoveStoreCloser_PermanentReopenFailureStaysPending(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	c, err := NewMoveStoreCloser(shards, barriers, nil, time.Millisecond)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	boom := errors.New("reopen failed")
	barriers.err = boom
	if err := c.OpenTenantStores(ctx, "acme"); !errors.Is(err, boom) {
		t.Fatalf("open = %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := c.RetryReopen(ctx, "acme"); !errors.Is(err, boom) {
			t.Fatalf("retry %d = %v, want the reopen error", i, err)
		}
		if c.HoldsTenant("acme") {
			t.Fatalf("acme held after retry %d", i)
		}
		if got := c.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
			t.Fatalf("pending after retry %d = %v", i, got)
		}
	}
	// A fresh close supersedes the pending reopen.
	barriers.err = nil
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := c.PendingReopen(); len(got) != 0 {
		t.Fatalf("pending after a fresh close = %v", got)
	}
	if err := c.OpenTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if !shards.started[StartedShard{TenantID: "acme", Database: "a"}] {
		t.Fatal("acme/a not served after the reopen")
	}
}

func TestMoveStoreCloser_Errors(t *testing.T) {
	if _, err := NewMoveStoreCloser(nil, nil, nil, 0); err == nil {
		t.Fatal("nil barriers accepted")
	}
	boom := errors.New("boom")
	shards := &fakeMoveShards{started: map[StartedShard]bool{{TenantID: "acme", Database: "a"}: true}}
	c, _ := NewMoveStoreCloser(shards, &fakeMoveBarriers{}, func(context.Context, string, string) (bool, error) {
		return false, boom
	}, 0)
	ctx := context.Background()
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.OpenTenantStores(ctx, "acme"); !errors.Is(err, boom) {
		t.Fatalf("open = %v, want the catch-up error", err)
	}
	never, _ := NewMoveStoreCloser(shards, &fakeMoveBarriers{}, func(context.Context, string, string) (bool, error) {
		return false, nil
	}, time.Millisecond)
	if err := never.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := never.OpenTenantStores(cctx, "acme"); !errors.Is(err, context.Canceled) {
		t.Fatalf("open cancelled = %v", err)
	}
	failing, _ := NewMoveStoreCloser(&fakeMoveShards{started: map[StartedShard]bool{}}, &fakeMoveBarriers{err: boom}, nil, 0)
	if err := failing.CloseTenantStores(ctx, "acme"); !errors.Is(err, boom) {
		t.Fatalf("barrier close = %v", err)
	}
	if err := failing.OpenTenantStores(ctx, "acme"); !errors.Is(err, boom) {
		t.Fatalf("barrier reopen = %v", err)
	}
}

type fakeMoveHosts struct {
	started map[string]bool
	calls   []string
}

func (f *fakeMoveHosts) Started(tenantID string) bool { return f.started[tenantID] }

func (f *fakeMoveHosts) Attach(_ context.Context, tenantID string) error {
	f.calls = append(f.calls, "attach "+tenantID)
	f.started[tenantID] = true
	return nil
}

func (f *fakeMoveHosts) Close(tenantID string) error {
	f.calls = append(f.calls, "close "+tenantID)
	delete(f.started, tenantID)
	return nil
}

func TestMoveStoreCloser_VolumeTenantHostWithoutShards(t *testing.T) {
	hosts := &fakeMoveHosts{started: map[string]bool{"acme": true}}
	barriers := &fakeMoveBarriers{}
	c, err := NewMoveStoreCloser(nil, barriers, nil, 0, WithMoveTenantHosts(hosts))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if hosts.started["acme"] {
		t.Fatal("host still started after close")
	}
	if err := c.OpenTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if want := []string{"close acme", "attach acme"}; !reflect.DeepEqual(hosts.calls, want) {
		t.Fatalf("host calls = %v, want %v", hosts.calls, want)
	}
	// A tenant whose Host was never started here is not started by a reopen.
	if err := c.CloseTenantStores(ctx, "other"); err != nil {
		t.Fatalf("close other: %v", err)
	}
	if err := c.OpenTenantStores(ctx, "other"); err != nil {
		t.Fatalf("open other: %v", err)
	}
	if hosts.started["other"] {
		t.Fatal("reopen started a host that was not running")
	}
}
