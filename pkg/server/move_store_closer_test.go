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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
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
	calls  []string
	err    error
	sealed map[string]bool
}

// TenantBarrierSealed makes the fake a MoveSealedBarriers: a tenant in
// sealed reopens sealed (a self-managed barrier awaiting its unseal).
func (f *fakeMoveBarriers) TenantBarrierSealed(tenantID string) bool {
	return f.sealed[tenantID]
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

// A self-managed tenant whose barrier reopens sealed is not reopened: the
// reopen answers *volume.TenantSealedError and stays pending, so the move
// step that called it keeps settling, and a retry once the administrator
// unseals attaches every closed shard and awaits its catch-up without a
// second barrier reopen.
func TestMoveStoreCloser_SealedReopenStaysPendingUntilUnseal(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
		{TenantID: "acme", Database: "b"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	var caughtUp []string
	c, err := NewMoveStoreCloser(shards, barriers, func(_ context.Context, tenantID, db string) (bool, error) {
		caughtUp = append(caughtUp, tenantID+"/"+db)
		return true, nil
	}, time.Millisecond)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	barriers.sealed = map[string]bool{"acme": true}
	err = c.OpenTenantStores(ctx, "acme")
	var sealed *volume.TenantSealedError
	if !errors.As(err, &sealed) || sealed.TenantID != "acme" {
		t.Fatalf("open of a sealed tenant = %v, want *volume.TenantSealedError for acme", err)
	}
	if got := c.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("pending = %v, want [acme]", got)
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme held after the sealed reopen")
	}
	if want := []string{"close acme/a", "close acme/b"}; !reflect.DeepEqual(shards.calls, want) {
		t.Fatalf("shard calls while sealed = %v, want %v", shards.calls, want)
	}
	if len(caughtUp) != 0 {
		t.Fatalf("catch-up awaited while sealed: %v", caughtUp)
	}
	// A retry while still sealed stays pending and attaches nothing.
	if err := c.RetryReopen(ctx, "acme"); !errors.As(err, &sealed) {
		t.Fatalf("sealed retry = %v, want *volume.TenantSealedError", err)
	}
	if want := []string{"close acme/a", "close acme/b"}; !reflect.DeepEqual(shards.calls, want) {
		t.Fatalf("shard calls after a sealed retry = %v, want %v", shards.calls, want)
	}
	// The administrator unseals: the retry attaches and awaits catch-up.
	barriers.sealed = nil
	if err := c.RetryReopen(ctx, "acme"); err != nil {
		t.Fatalf("retry after the unseal: %v", err)
	}
	if got := c.PendingReopen(); len(got) != 0 {
		t.Fatalf("pending after the unseal = %v", got)
	}
	if want := []string{"close acme/a", "close acme/b", "attach acme/a", "attach acme/b"}; !reflect.DeepEqual(shards.calls, want) {
		t.Fatalf("shard calls = %v, want %v", shards.calls, want)
	}
	if want := []string{"acme/a", "acme/b"}; !reflect.DeepEqual(caughtUp, want) {
		t.Fatalf("caught up = %v, want %v", caughtUp, want)
	}
	if want := []string{"close acme", "reopen acme"}; !reflect.DeepEqual(barriers.calls, want) {
		t.Fatalf("barrier calls = %v, want %v", barriers.calls, want)
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

// A replica that never catches up fails the reopen once the catch-up
// timeout passes, so the settle step fails instead of polling forever, and
// the tenant stays pending for RetryReopen.
func TestMoveStoreCloser_CatchUpTimeout(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{{TenantID: "acme", Database: "a"}: true}}
	never, err := NewMoveStoreCloser(shards, &fakeMoveBarriers{}, func(context.Context, string, string) (bool, error) {
		return false, nil
	}, time.Millisecond, WithMoveCatchUpTimeout(5*time.Millisecond))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := never.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	err = never.OpenTenantStores(ctx, "acme")
	if !errors.Is(err, ErrMoveCatchUpTimeout) {
		t.Fatalf("open = %v, want ErrMoveCatchUpTimeout", err)
	}
	if got := never.PendingReopen(); len(got) != 1 || got[0] != "acme" {
		t.Fatal("timed-out reopen not left pending")
	}
	if never.catchUpTimeout != 5*time.Millisecond {
		t.Fatalf("catchUpTimeout = %v", never.catchUpTimeout)
	}
	def, _ := NewMoveStoreCloser(shards, &fakeMoveBarriers{}, nil, 0, WithMoveCatchUpTimeout(0))
	if def.catchUpTimeout != DefaultMoveCatchUpTimeout {
		t.Fatalf("default catchUpTimeout = %v", def.catchUpTimeout)
	}
}

type fakeMoveHosts struct {
	started   map[string]bool
	volume    map[string]bool
	lookupErr error
	attachErr error
	calls     []string
}

func (f *fakeMoveHosts) Started(tenantID string) bool { return f.started[tenantID] }

func (f *fakeMoveHosts) HostsTenant(_ context.Context, tenantID string) (bool, error) {
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	return f.volume[tenantID], nil
}

func (f *fakeMoveHosts) Attach(_ context.Context, tenantID string) error {
	f.calls = append(f.calls, "attach "+tenantID)
	if f.attachErr != nil {
		return f.attachErr
	}
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

// placedReplicas returns a MoveLocalReplicas answering dbs for every tenant.
func placedReplicas(dbs ...string) MoveLocalReplicas {
	return func(context.Context, string) ([]string, error) {
		return append([]string(nil), dbs...), nil
	}
}

func TestMoveStoreCloser_ResumedSettleReopensPlacedReplicas(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{}}
	barriers := &fakeMoveBarriers{}
	// Each catch-up read takes one answer from answers and reports it on
	// asked, so the test sees the reopen still waiting on a false answer.
	answers := make(chan bool)
	asked := make(chan string, 8)
	caughtUp := func(_ context.Context, _, db string) (bool, error) {
		asked <- db
		return <-answers, nil
	}
	c, err := NewMoveStoreCloser(shards, barriers, caughtUp, time.Millisecond,
		WithMoveLocalReplicas(placedReplicas("a", "b")))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.OpenTenantStores(context.Background(), "acme") }()
	for _, step := range []struct {
		db     string
		answer bool
	}{{"a", false}, {"a", true}, {"b", false}, {"b", true}} {
		if got := <-asked; got != step.db {
			t.Fatalf("catch-up read for %q, want %q", got, step.db)
		}
		select {
		case err := <-done:
			t.Fatalf("open returned %v before %s caught up", err, step.db)
		default:
		}
		answers <- step.answer
	}
	if err := <-done; err != nil {
		t.Fatalf("open: %v", err)
	}
	if want := []string{"attach acme/a", "attach acme/b"}; !reflect.DeepEqual(shards.calls, want) {
		t.Fatalf("shard calls = %v, want %v", shards.calls, want)
	}
	if want := []string{"reopen acme"}; !reflect.DeepEqual(barriers.calls, want) {
		t.Fatalf("barrier calls = %v, want %v", barriers.calls, want)
	}
	if got := c.PendingReopen(); len(got) != 0 {
		t.Fatalf("pending = %v", got)
	}
}

// resumedVolumeCloser builds a closer with no close record, as after a
// restart, in standalone mode (no shards) or raft mode (placement lists no
// databases for a volume tenant).
func resumedVolumeCloser(t *testing.T, mode string, hosts *fakeMoveHosts) *MoveStoreCloser {
	t.Helper()
	opts := []MoveStoreCloserOption{WithMoveTenantHosts(hosts)}
	var shards MoveShardLifecycle
	if mode == "raft" {
		shards = &fakeMoveShards{started: map[StartedShard]bool{}}
		opts = append(opts, WithMoveLocalReplicas(placedReplicas()))
	}
	c, err := NewMoveStoreCloser(shards, &fakeMoveBarriers{}, nil, 0, opts...)
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	return c
}

func TestMoveStoreCloser_ResumedSettleAttachesVolumeTenantHost(t *testing.T) {
	for _, mode := range []string{"standalone", "raft"} {
		t.Run(mode, func(t *testing.T) {
			hosts := &fakeMoveHosts{started: map[string]bool{}, volume: map[string]bool{"acme": true}}
			c := resumedVolumeCloser(t, mode, hosts)
			if err := c.OpenTenantStores(context.Background(), "acme"); err != nil {
				t.Fatalf("open: %v", err)
			}
			if want := []string{"attach acme"}; !reflect.DeepEqual(hosts.calls, want) {
				t.Fatalf("host calls = %v, want %v", hosts.calls, want)
			}
			if got := c.PendingReopen(); len(got) != 0 {
				t.Fatalf("pending = %v", got)
			}
		})
	}
}

func TestMoveStoreCloser_ResumedSettleRebuildsStartedVolumeTenantHost(t *testing.T) {
	for _, mode := range []string{"standalone", "raft"} {
		t.Run(mode, func(t *testing.T) {
			hosts := &fakeMoveHosts{started: map[string]bool{"acme": true}, volume: map[string]bool{"acme": true}}
			c := resumedVolumeCloser(t, mode, hosts)
			if err := c.OpenTenantStores(context.Background(), "acme"); err != nil {
				t.Fatalf("open: %v", err)
			}
			if want := []string{"close acme", "attach acme"}; !reflect.DeepEqual(hosts.calls, want) {
				t.Fatalf("host calls = %v, want %v", hosts.calls, want)
			}
		})
	}
}

func TestMoveStoreCloser_ResumedSettleUnopenableHostStaysPending(t *testing.T) {
	for _, mode := range []string{"standalone", "raft"} {
		t.Run(mode, func(t *testing.T) {
			attachErr := errors.New("host attach failed")
			hosts := &fakeMoveHosts{started: map[string]bool{}, volume: map[string]bool{"acme": true}, attachErr: attachErr}
			c := resumedVolumeCloser(t, mode, hosts)
			if err := c.OpenTenantStores(context.Background(), "acme"); !errors.Is(err, attachErr) {
				t.Fatalf("open: err = %v, want %v", err, attachErr)
			}
			if got := c.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
				t.Fatalf("pending = %v, want [acme]", got)
			}
		})
	}
}

func TestMoveStoreCloser_ResumedSettleHostLookupFailureStaysPending(t *testing.T) {
	lookupErr := errors.New("tenant metadata unavailable")
	hosts := &fakeMoveHosts{started: map[string]bool{}, lookupErr: lookupErr}
	c := resumedVolumeCloser(t, "standalone", hosts)
	if err := c.OpenTenantStores(context.Background(), "acme"); !errors.Is(err, lookupErr) {
		t.Fatalf("open: err = %v, want %v", err, lookupErr)
	}
	if got := c.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("pending = %v, want [acme]", got)
	}
}

type fakeIsolationLookup struct {
	tenants map[string]*tenant.TenantInfo
	err     error
}

func (f fakeIsolationLookup) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tenants[tenantID], nil
}

func TestTenantHostRegistry_HostsTenantReadsTenantIsolation(t *testing.T) {
	ctx := context.Background()
	lookup := fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"vol":  {ID: "vol", IsolationType: tenant.IsolationVolume},
		"raft": {ID: "raft", IsolationType: tenant.IsolationRaft},
	}}
	reg := NewTenantHostRegistryFromLookup(&seal.BarrierRegistry{}, lookup, volTestLogger())
	for id, want := range map[string]bool{"vol": true, "raft": false, "none": false} {
		got, err := reg.HostsTenant(ctx, id)
		if err != nil || got != want {
			t.Fatalf("HostsTenant(%q) = %v, %v; want %v", id, got, err, want)
		}
	}
	lookupErr := errors.New("metadata unavailable")
	failing := NewTenantHostRegistryFromLookup(&seal.BarrierRegistry{}, fakeIsolationLookup{err: lookupErr}, volTestLogger())
	if _, err := failing.HostsTenant(ctx, "vol"); !errors.Is(err, lookupErr) {
		t.Fatalf("HostsTenant with a failing lookup: err = %v, want %v", err, lookupErr)
	}
}

func TestMoveStoreCloser_ResumedSettleLookupFailureStaysPending(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{}}
	barriers := &fakeMoveBarriers{}
	lookupErr := errors.New("placement unavailable")
	failLookup := true
	lookup := func(context.Context, string) ([]string, error) {
		if failLookup {
			return nil, lookupErr
		}
		return []string{"a"}, nil
	}
	reads := 0
	c, err := NewMoveStoreCloser(shards, barriers, func(context.Context, string, string) (bool, error) {
		reads++
		return true, nil
	}, time.Millisecond, WithMoveLocalReplicas(lookup))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := c.OpenTenantStores(ctx, "acme"); !errors.Is(err, lookupErr) {
		t.Fatalf("open = %v, want the lookup error", err)
	}
	if got := c.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("pending = %v, want [acme]", got)
	}
	if len(shards.calls) != 0 || reads != 0 {
		t.Fatalf("shard calls = %v, catch-up reads = %d after a failed lookup", shards.calls, reads)
	}
	failLookup = false
	if err := c.RetryReopen(ctx, "acme"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := c.PendingReopen(); len(got) != 0 {
		t.Fatalf("pending after the retry = %v", got)
	}
	if want := []string{"attach acme/a"}; !reflect.DeepEqual(shards.calls, want) || reads != 1 {
		t.Fatalf("shard calls = %v, catch-up reads = %d", shards.calls, reads)
	}
}

func TestMoveStoreCloser_CloseRecordsUnstartedPlacedReplica(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
	}}
	barriers := &fakeMoveBarriers{}
	var asked []string
	c, err := NewMoveStoreCloser(shards, barriers, func(_ context.Context, _, db string) (bool, error) {
		asked = append(asked, db)
		return true, nil
	}, time.Millisecond, WithMoveLocalReplicas(placedReplicas("a", "b")))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	ctx := context.Background()
	if err := c.CloseTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if want := []string{"close acme/a"}; !reflect.DeepEqual(shards.calls, want) {
		t.Fatalf("shard calls after close = %v, want %v", shards.calls, want)
	}
	if err := c.OpenTenantStores(ctx, "acme"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if want := []string{"close acme/a", "attach acme/a", "attach acme/b"}; !reflect.DeepEqual(shards.calls, want) {
		t.Fatalf("shard calls = %v, want %v", shards.calls, want)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(asked, want) {
		t.Fatalf("catch-up reads = %v, want %v", asked, want)
	}
}

func TestMoveStoreCloser_CloseLookupFailureFailsClose(t *testing.T) {
	shards := &fakeMoveShards{started: map[StartedShard]bool{
		{TenantID: "acme", Database: "a"}: true,
	}}
	lookupErr := errors.New("placement unavailable")
	c, err := NewMoveStoreCloser(shards, &fakeMoveBarriers{}, func(context.Context, string, string) (bool, error) {
		return true, nil
	}, time.Millisecond, WithMoveLocalReplicas(func(context.Context, string) ([]string, error) {
		return nil, lookupErr
	}))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	if err := c.CloseTenantStores(context.Background(), "acme"); !errors.Is(err, lookupErr) {
		t.Fatalf("close = %v, want the lookup error", err)
	}
	if c.HoldsTenant("acme") {
		t.Fatal("acme held after a failed close")
	}
	if len(shards.calls) != 0 {
		t.Fatalf("shard calls = %v after a failed lookup", shards.calls)
	}
}

func TestMoveStoreCloser_AttachRaceWithReconcilerCountsStarted(t *testing.T) {
	// The shard reconciler starts "a" while the reopen attaches it: Attach
	// fails as already started, and ListStarted now reports it.
	shards := &fakeMoveShards{started: map[StartedShard]bool{}, err: errors.New("shard already started")}
	reads := 0
	c, err := NewMoveStoreCloser(shards, &fakeMoveBarriers{}, func(context.Context, string, string) (bool, error) {
		reads++
		return true, nil
	}, time.Millisecond, WithMoveLocalReplicas(placedReplicas("a")))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	if err := c.OpenTenantStores(context.Background(), "acme"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if reads != 1 {
		t.Fatalf("catch-up reads = %d, want 1", reads)
	}
	// An Attach that fails with the shard still not started fails the
	// reopen and leaves the tenant pending.
	refused := errors.New("attach refused")
	c, err = NewMoveStoreCloser(refusingAttachShards{&fakeMoveShards{started: map[StartedShard]bool{}}, refused},
		&fakeMoveBarriers{}, func(context.Context, string, string) (bool, error) {
			return true, nil
		}, time.Millisecond, WithMoveLocalReplicas(placedReplicas("a")))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	if err := c.OpenTenantStores(context.Background(), "acme"); !errors.Is(err, refused) {
		t.Fatalf("open = %v, want the attach error", err)
	}
	if got := c.PendingReopen(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("pending = %v, want [acme]", got)
	}
}

// refusingAttachShards refuses every Attach without starting the shard.
type refusingAttachShards struct {
	*fakeMoveShards
	err error
}

func (r refusingAttachShards) Attach(context.Context, string, string) error { return r.err }
