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
	"slices"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// ---------------------------------------------------------------------------
// NewLeaseService
// ---------------------------------------------------------------------------

func TestLeaseService_NewLeaseService_ReturnsNonNil(t *testing.T) {
	svc := NewLeaseService(&mockHost{}, 1)
	if svc == nil {
		t.Fatal("expected non-nil LeaseService")
	}
}

func TestLeaseService_NewLeaseService_PreservesShardID(t *testing.T) {
	var capturedProposeShardID uint64
	var capturedReadShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedProposeShardID = shardID
			return sm.Result{Value: 42}, nil
		},
		syncReadFn: func(_ context.Context, shardID uint64, _ interface{}) (interface{}, error) {
			capturedReadShardID = shardID
			return &Lease{ID: 42, TTL: 60, GrantedTTL: 60, RemainingTTL: 60}, nil
		},
	}

	svc := NewLeaseService(host, 77)
	_, err := svc.Grant(context.Background(), 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedProposeShardID != 77 {
		t.Fatalf("expected propose shardID 77, got %d", capturedProposeShardID)
	}
	if _, err := svc.TimeToLive(context.Background(), 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReadShardID != 77 {
		t.Fatalf("expected read shardID 77, got %d", capturedReadShardID)
	}
}

// ---------------------------------------------------------------------------
// Grant
// ---------------------------------------------------------------------------

func TestLeaseService_Grant_Success(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 100}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			t.Fatalf("Grant read back %T; the apply already answered what it granted", query)
			return nil, nil
		},
	}

	svc := NewLeaseService(host, 1)
	lease, err := svc.Grant(context.Background(), 60)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if lease == nil {
		t.Fatal("expected non-nil lease")
	}
	if lease.ID != 100 {
		t.Fatalf("expected lease ID 100, got %d", lease.ID)
	}
	if lease.TTL != 60 {
		t.Fatalf("expected TTL 60, got %d", lease.TTL)
	}
	if lease.GrantedTTL != 60 {
		t.Fatalf("expected GrantedTTL 60, got %d", lease.GrantedTTL)
	}
	// BUG 2 regression: RemainingTTL must be what the Lessor granted, not
	// stay at its zero value.
	if lease.RemainingTTL != 60 {
		t.Fatalf("expected RemainingTTL 60, got %d", lease.RemainingTTL)
	}
}

// TestLeaseService_Grant_AnswersTheLeaseItsApplyGranted pins that a Grant whose
// proposal applied answers with the lease that apply granted, even when the
// expiry sweep has already revoked it by the time the answer is built. A 1s
// lease on a loaded node can be swept between the apply and any later read;
// reading it back turned a successful grant into "lease not found" for a lease
// the caller was never told it held (DS-1689).
func TestLeaseService_Grant_AnswersTheLeaseItsApplyGranted(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 7}, nil
		},
		// What a read after the sweep revoked lease 7 answers.
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &LeaseNotFoundError{LeaseID: 7}
		},
	}

	svc := NewLeaseService(host, 1)
	lease, err := svc.Grant(context.Background(), 1)
	if err != nil {
		t.Fatalf("Grant of an applied lease: got %v, want the granted lease", err)
	}
	want := Lease{ID: 7, TTL: 1, GrantedTTL: 1, RemainingTTL: 1}
	if lease == nil || lease.ID != want.ID || lease.TTL != want.TTL ||
		lease.GrantedTTL != want.GrantedTTL || lease.RemainingTTL != want.RemainingTTL || len(lease.Keys) != 0 {
		t.Fatalf("Grant = %+v, want %+v", lease, want)
	}
}

func TestLeaseService_Grant_ProposeError(t *testing.T) {
	sentinel := errors.New("raft timeout")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.Grant(context.Background(), 60)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel error, got %v", pe.Unwrap())
	}
}

func TestLeaseService_Grant_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       5,
				LeaderID:      3,
				LeaderAddress: "10.0.0.3:9100",
			}
		},
	}

	svc := NewLeaseService(host, 5)
	_, err := svc.Grant(context.Background(), 60)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 5 {
		t.Fatalf("expected ShardID 5, got %d", nle.ShardID)
	}
	if nle.LeaderID != 3 {
		t.Fatalf("expected LeaderID 3, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "10.0.0.3:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.3:9100, got %s", nle.LeaderAddress)
	}
}

// ---------------------------------------------------------------------------
// Revoke
// ---------------------------------------------------------------------------

func TestLeaseService_Revoke_Success(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 2}, nil
		},
	}

	svc := NewLeaseService(host, 1)
	err := svc.Revoke(context.Background(), 42)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestLeaseService_Revoke_ProposeError(t *testing.T) {
	sentinel := errors.New("connection refused")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewLeaseService(host, 1)
	err := svc.Revoke(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

func TestLeaseService_Revoke_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       8,
				LeaderID:      1,
				LeaderAddress: "10.0.0.1:9100",
			}
		},
	}

	svc := NewLeaseService(host, 8)
	err := svc.Revoke(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 8 {
		t.Fatalf("expected ShardID 8, got %d", nle.ShardID)
	}
}

// ---------------------------------------------------------------------------
// KeepAlive
// ---------------------------------------------------------------------------

// A leader renews in memory: one linearizable read, no proposal.
func TestLeaseService_KeepAlive_RenewsLocallyWithoutProposing(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			t.Fatal("a leader-local renewal must not propose")
			return sm.Result{}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(LeaseKeepAliveQuery)
			if !ok {
				t.Fatalf("expected LeaseKeepAliveQuery, got %T", query)
			}
			if q.ID != 42 {
				t.Fatalf("expected lease ID 42, got %d", q.ID)
			}
			return &Lease{ID: 42, TTL: 60, GrantedTTL: 60, RemainingTTL: 60}, nil
		},
	}

	got, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got.ID != 42 || got.TTL != 60 || got.RemainingTTL != 60 {
		t.Fatalf("unexpected lease %+v", got)
	}
}

// A replica that is not the primary renews through the log and answers from
// the committed result, with no read-back (go-qrdb#275).
func TestLeaseService_KeepAlive_NotPrimaryFallsBackToTheLog(t *testing.T) {
	var proposed bool
	host := &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			proposed = true
			return sm.Result{
				Value: 42,
				Data:  protocol.EncodeLeaseInfo(42, 60, 58, [][]byte{[]byte("key1")}),
			}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			if _, ok := query.(LeaseKeepAliveQuery); ok {
				return nil, &lease.NotPrimaryError{}
			}
			t.Fatalf("a log-backed renewal must not read back; got query %T", query)
			return nil, nil
		},
	}

	got, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !proposed {
		t.Fatal("expected the renewal to be proposed")
	}
	if got.ID != 42 || got.TTL != 60 || got.RemainingTTL != 58 {
		t.Fatalf("unexpected lease %+v", got)
	}
	if len(got.Keys) != 1 || string(got.Keys[0]) != "key1" {
		t.Fatalf("expected keys [key1], got %v", got.Keys)
	}
}

// The log-backed renewal of a lease the lessor does not hold is an idempotent
// apply success carrying the not-found marker, reported as *LeaseNotFoundError.
func TestLeaseService_KeepAlive_NotPrimaryNotFound(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			return sm.Result{Value: 0, Data: protocol.EncodeLeaseKeepAliveNotFound(42)}, nil
		},
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			return nil, &lease.NotPrimaryError{}
		},
	}

	_, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	var lnf *LeaseNotFoundError
	if !errors.As(err, &lnf) || lnf.LeaseID != 42 {
		t.Fatalf("expected *LeaseNotFoundError{42}, got %T: %v", err, err)
	}
}

// A committed result that carries neither lease info nor the not-found marker
// is refused rather than answered with a zero lease.
func TestLeaseService_KeepAlive_NotPrimaryUndecodableResult(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			return sm.Result{Value: 42}, nil
		},
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			return nil, &lease.NotPrimaryError{}
		},
	}

	_, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	var ur *unexpectedResultTypeError
	if !errors.As(err, &ur) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

func TestLeaseService_KeepAlive_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("proposal dropped")
		},
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			return nil, &lease.NotPrimaryError{}
		},
	}

	_, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestLeaseService_KeepAlive_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			return nil, errors.New("state machine error")
		},
	}

	_, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestLeaseService_KeepAlive_UnknownLeaseIsTyped(t *testing.T) {
	for name, cause := range map[string]error{
		"lessor":  &lease.NotFoundError{ID: 42},
		"service": &LeaseNotFoundError{LeaseID: 42},
	} {
		t.Run(name, func(t *testing.T) {
			host := &mockHost{
				syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
					return nil, cause
				},
			}
			_, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
			var lnf *LeaseNotFoundError
			if !errors.As(err, &lnf) || lnf.LeaseID != 42 {
				t.Fatalf("expected *LeaseNotFoundError{42}, got %T: %v", err, err)
			}
		})
	}
}

func TestLeaseService_KeepAlive_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			return "not a lease", nil
		},
	}
	_, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	var ur *unexpectedResultTypeError
	if !errors.As(err, &ur) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

// A lease the leader has checkpointed is renewed through the log, whose apply
// clears the checkpoint.
func TestLeaseService_KeepAlive_CheckpointedFallsBackToTheLog(t *testing.T) {
	var proposed bool
	host := &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			proposed = true
			return sm.Result{Value: 42, Data: protocol.EncodeLeaseInfo(42, 600, 600, nil)}, nil
		},
		syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
			return nil, &lease.CheckpointedError{ID: 42}
		},
	}

	got, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !proposed || got.RemainingTTL != 600 {
		t.Fatalf("proposed = %v, lease = %+v; want a replicated renewal to 600s", proposed, got)
	}
}

func TestNewLeaseCheckpointer_ProposesTheBatchInBoundedCommands(t *testing.T) {
	var cmds [][]byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, c []byte) (sm.Result, error) {
			if shardID != 7 {
				t.Fatalf("shard = %d, want 7", shardID)
			}
			cmds = append(cmds, c)
			return sm.Result{}, nil
		},
	}
	cps := make([]lease.Checkpoint, protocol.MaxLeaseCheckpointBatch+1)
	for i := range cps {
		cps[i] = lease.Checkpoint{ID: int64(i + 1), RemainingTTL: 30}
	}
	if err := NewLeaseCheckpointer(host, 7).Checkpoint(context.Background(), 9, 31, cps); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("proposed %d commands, want 2", len(cmds))
	}
	var got []lease.Checkpoint
	for _, c := range cmds {
		cmd, err := protocol.DecodeCommand(c)
		if err != nil || cmd.Op != protocol.OpLeaseCheckpoint {
			t.Fatalf("proposed %x, want a lease checkpoint (%v)", c, err)
		}
		token, index, batch, err := protocol.DecodeLeaseCheckpoint(cmd.Payload)
		if err != nil || token != 9 || index != 31 {
			t.Fatalf("token = %d, index = %d, err = %v; want 9 and 31", token, index, err)
		}
		got = append(got, batch...)
	}
	if !slices.Equal(got, cps) {
		t.Fatal("the proposed commands do not carry the batch in order")
	}
}

func TestNewLeaseCheckpointer_StopsAtTheFirstError(t *testing.T) {
	for name, tc := range map[string]struct {
		result sm.Result
		err    error
		want   any
	}{
		"propose": {err: errors.New("dropped"), want: new(*ProposeError)},
		"refusal": {result: statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), want: new(*ProposeError)},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			host := &mockHost{
				syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
					calls++
					return tc.result, tc.err
				},
			}
			cps := make([]lease.Checkpoint, protocol.MaxLeaseCheckpointBatch+1)
			err := NewLeaseCheckpointer(host, 1).Checkpoint(context.Background(), 1, 1, cps)
			if !errors.As(err, tc.want) {
				t.Fatalf("got %T: %v", err, err)
			}
			if calls != 1 {
				t.Fatalf("proposed %d commands after the first failed, want 1", calls)
			}
		})
	}
}

func TestLeaseService_CheckpointEnabledFollowsTheFeatureGate(t *testing.T) {
	if !NewLeaseCheckpointer(&mockHost{}, 1).CheckpointEnabled() {
		t.Fatal("a host with no feature gate must allow checkpoints")
	}
	gated := &gatedHost{mockHost: &mockHost{}, enabled: map[uint64]bool{2: true}}
	if NewLeaseCheckpointer(gated, 1).CheckpointEnabled() {
		t.Fatal("checkpoints allowed in a group whose gate is closed")
	}
	if !NewLeaseCheckpointer(gated, 2).CheckpointEnabled() {
		t.Fatal("checkpoints refused in a group whose gate is open")
	}
	if !slices.Equal(gated.asked, []featuregate.Feature{featuregate.LeaseCheckpoint, featuregate.LeaseCheckpoint}) {
		t.Fatalf("asked the gate about %v, want lease-checkpoint", gated.asked)
	}
}

// Until every replica of the group can apply a lease checkpoint, a renewal
// goes through the log even on the leader: a replica that predates the
// lease-checkpoint feature counts leases only from the log, so a renewal
// made in the leader's memory would be invisible to it, and on becoming
// leader it would revoke a lease its holder was keeping alive.
func TestLeaseService_KeepAlive_GateClosedRenewsThroughTheLog(t *testing.T) {
	var proposed bool
	host := &gatedHost{
		mockHost: &mockHost{
			syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
				proposed = true
				return sm.Result{Value: 42, Data: protocol.EncodeLeaseInfo(42, 60, 60, nil)}, nil
			},
			syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
				t.Fatalf("a renewal in a group whose lease-checkpoint gate is closed must not renew in memory; got query %T", query)
				return nil, nil
			},
		},
		enabled: map[uint64]bool{},
	}

	got, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	if !proposed {
		t.Fatal("KeepAlive did not propose the renewal")
	}
	if got.ID != 42 || got.RemainingTTL != 60 {
		t.Fatalf("lease = %+v, want the committed renewal of 42", got)
	}
	if !slices.Equal(host.asked, []featuregate.Feature{featuregate.LeaseCheckpoint}) {
		t.Fatalf("asked the gate about %v, want lease-checkpoint", host.asked)
	}
}

// Once the gate is open, the leader renews in memory as before.
func TestLeaseService_KeepAlive_GateOpenRenewsLocally(t *testing.T) {
	host := &gatedHost{
		mockHost: &mockHost{
			syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
				t.Fatal("a leader-local renewal must not propose")
				return sm.Result{}, nil
			},
			syncReadFn: func(context.Context, uint64, interface{}) (interface{}, error) {
				return &Lease{ID: 42, TTL: 60, GrantedTTL: 60, RemainingTTL: 60}, nil
			},
		},
		enabled: map[uint64]bool{1: true},
	}

	got, err := NewLeaseService(host, 1).KeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	if got.ID != 42 || got.RemainingTTL != 60 {
		t.Fatalf("lease = %+v, want the local renewal of 42", got)
	}
}

func TestLeaseService_CheckpointAll(t *testing.T) {
	answered := func(err error) <-chan error {
		ch := make(chan error, 1)
		ch <- err
		return ch
	}
	boom := errors.New("not applied")
	for name, tc := range map[string]struct {
		result   any
		err      error
		canceled bool
		check    func(error) bool
	}{
		"applied":     {result: answered(nil), check: func(err error) bool { return err == nil }},
		"failed":      {result: answered(boom), check: func(err error) bool { return errors.Is(err, boom) }},
		"not primary": {err: &lease.NotPrimaryError{}, check: func(err error) bool { var np *lease.NotPrimaryError; return errors.As(err, &np) }},
		"read error":  {err: errors.New("closed"), check: func(err error) bool { var re *ReadError; return errors.As(err, &re) }},
		"unexpected":  {result: "no", check: func(err error) bool { var ur *unexpectedResultTypeError; return errors.As(err, &ur) }},
		"abandoned": {result: make(<-chan error), canceled: true,
			check: func(err error) bool { return errors.Is(err, context.Canceled) }},
	} {
		t.Run(name, func(t *testing.T) {
			host := &mockHost{
				staleReadFn: func(_ context.Context, shardID uint64, query any) (any, error) {
					if _, ok := query.(LeaseCheckpointNowQuery); !ok || shardID != 3 {
						t.Fatalf("read %T on shard %d, want LeaseCheckpointNowQuery on 3", query, shardID)
					}
					return tc.result, tc.err
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			svc := &LeaseService{host: host, shardID: 3}
			if err := svc.CheckpointAll(ctx); !tc.check(err) {
				t.Fatalf("CheckpointAll = %T: %v", err, err)
			}
		})
	}
}

func TestLeaseService_AwaitApplied(t *testing.T) {
	for name, tc := range map[string]struct {
		err   error
		check func(error) bool
	}{
		"applied":    {check: func(err error) bool { return err == nil }},
		"read error": {err: errors.New("read index not ready"), check: func(err error) bool { var re *ReadError; return errors.As(err, &re) }},
	} {
		t.Run(name, func(t *testing.T) {
			reads := 0
			host := &mockHost{
				syncReadFn: func(_ context.Context, shardID uint64, query any) (any, error) {
					reads++
					if _, ok := query.(LeasesQuery); !ok || shardID != 3 {
						t.Fatalf("read %T on shard %d, want a linearizable LeasesQuery on 3", query, shardID)
					}
					return []*Lease{}, tc.err
				},
			}
			svc := &LeaseService{host: host, shardID: 3}
			if err := svc.AwaitApplied(context.Background()); !tc.check(err) {
				t.Fatalf("AwaitApplied = %T: %v", err, err)
			}
			if reads != 1 {
				t.Fatalf("AwaitApplied made %d linearizable reads, want 1", reads)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TimeToLive
// ---------------------------------------------------------------------------

func TestLeaseService_TimeToLive_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(LeaseTimeToLiveQuery)
			if !ok {
				t.Fatalf("expected LeaseTimeToLiveQuery, got %T", query)
			}
			return &Lease{
				ID:           q.ID,
				TTL:          120,
				GrantedTTL:   120,
				RemainingTTL: 115,
				Keys:         [][]byte{[]byte("session/a"), []byte("session/b")},
			}, nil
		},
	}

	svc := NewLeaseService(host, 1)
	lease, err := svc.TimeToLive(context.Background(), 7)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if lease.ID != 7 {
		t.Fatalf("expected lease ID 7, got %d", lease.ID)
	}
	if lease.TTL != 120 {
		t.Fatalf("expected TTL 120, got %d", lease.TTL)
	}
	if lease.RemainingTTL != 115 {
		t.Fatalf("expected RemainingTTL 115, got %d", lease.RemainingTTL)
	}
	if len(lease.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(lease.Keys))
	}
}

func TestLeaseService_TimeToLive_ReadError(t *testing.T) {
	sentinel := errors.New("read timeout")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.TimeToLive(context.Background(), 7)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

// TestLeaseService_TimeToLive_LeaseNotFoundUnwraps pins go-qrdb#387 at the
// service boundary: when the underlying Lookup (engine.MVCCEngine, in
// production) reports a missing lease as *service.LeaseNotFoundError, the
// service layer's ReadError wrapper must still let errors.As find it, since
// every transport's error mapping (REST, gRPC, QUIC, MCP) keys off exactly
// that type rather than ReadError itself.
func TestLeaseService_TimeToLive_LeaseNotFoundUnwraps(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &LeaseNotFoundError{LeaseID: 7}
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.TimeToLive(context.Background(), 7)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var lnf *LeaseNotFoundError
	if !errors.As(err, &lnf) {
		t.Fatalf("expected *LeaseNotFoundError to unwrap from %T: %v", err, err)
	}
	if lnf.LeaseID != 7 {
		t.Errorf("LeaseID = %d, want 7", lnf.LeaseID)
	}
}

func TestLeaseService_TimeToLive_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       3,
				LeaderID:      5,
				LeaderAddress: "10.0.0.5:9100",
			}
		},
	}

	svc := NewLeaseService(host, 3)
	_, err := svc.TimeToLive(context.Background(), 7)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 3 {
		t.Fatalf("expected ShardID 3, got %d", nle.ShardID)
	}
	if nle.LeaderID != 5 {
		t.Fatalf("expected LeaderID 5, got %d", nle.LeaderID)
	}
}

// TestLeaseService_TimeToLive_FollowerNamesTheLeader pins review 5188: a
// follower's deadline lags the leader's in-memory renewals, so its replica
// answers *lease.StaleTTLError and the service refuses with the leader's
// replica ID and Raft address for the transports to forward to, keeping the
// replica's view of the lease for an ownership check.
func TestLeaseService_TimeToLive_FollowerNamesTheLeader(t *testing.T) {
	held := &Lease{ID: 7, TTL: 10, GrantedTTL: 10, RemainingTTL: 2, Keys: [][]byte{[]byte("k")}}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &lease.StaleTTLError{Lease: held}
		},
		getLeaderIDFn: func(uint64) (uint64, bool, error) { return 2, true, nil },
		memberships: map[uint64]*quicraft.Membership{
			3: {Nodes: map[uint64]string{1: "10.0.0.1:9100", 2: "10.0.0.2:9100"}},
		},
	}

	l, err := NewLeaseService(host, 3).TimeToLive(context.Background(), 7)
	if l != nil {
		t.Fatalf("follower answered a remaining TTL: %+v", l)
	}
	var stale *LeaseTTLNotLeaderError
	if !errors.As(err, &stale) {
		t.Fatalf("error = %T: %v, want *LeaseTTLNotLeaderError", err, err)
	}
	if stale.Lease != held {
		t.Fatalf("refusal lease = %+v, want the replica's view %+v", stale.Lease, held)
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("refusal does not unwrap to *NotLeaderError: %v", err)
	}
	if nle.ShardID != 3 || nle.LeaderID != 2 || nle.LeaderAddress != "10.0.0.2:9100" {
		t.Fatalf("not-leader = %+v, want shard 3 leader 2 at 10.0.0.2:9100", nle)
	}
	if got, err := LeaseExists(nil, err); err != nil || got != held {
		t.Fatalf("LeaseExists = %v, %v; want the replica's view", got, err)
	}
}

func TestLeaseService_TimeToLive_FollowerWithoutALeader(t *testing.T) {
	for name, host := range map[string]*mockHost{
		"no leader": {getLeaderIDFn: func(uint64) (uint64, bool, error) { return 0, false, nil }},
		"no membership": {
			getLeaderIDFn: func(uint64) (uint64, bool, error) { return 2, true, nil },
			membershipErr: errors.New("membership unavailable"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			host.syncReadFn = func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
				return nil, &lease.StaleTTLError{Lease: &Lease{ID: 7}}
			}
			_, err := NewLeaseService(host, 3).TimeToLive(context.Background(), 7)
			var nle *NotLeaderError
			if !errors.As(err, &nle) {
				t.Fatalf("error = %T: %v, want *NotLeaderError", err, err)
			}
			if nle.LeaderAddress != "" {
				t.Fatalf("LeaderAddress = %q, want empty", nle.LeaderAddress)
			}
		})
	}
}

func TestLeaseExists_PassesOtherAnswersThrough(t *testing.T) {
	l := &Lease{ID: 1}
	if got, err := LeaseExists(l, nil); got != l || err != nil {
		t.Fatalf("LeaseExists(lease, nil) = %v, %v", got, err)
	}
	nf := &LeaseNotFoundError{LeaseID: 1}
	if got, err := LeaseExists(nil, nf); got != nil || err != nf {
		t.Fatalf("LeaseExists(nil, not found) = %v, %v", got, err)
	}
}

func TestLeaseService_TimeToLive_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			// Return a string instead of *Lease.
			return "not-a-lease", nil
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.TimeToLive(context.Background(), 7)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var urte *unexpectedResultTypeError
	if !errors.As(err, &urte) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Leases
// ---------------------------------------------------------------------------

func TestLeaseService_Leases_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			if _, ok := query.(LeasesQuery); !ok {
				t.Fatalf("expected LeasesQuery, got %T", query)
			}
			return []*Lease{
				{ID: 1, TTL: 30, GrantedTTL: 30, RemainingTTL: 25},
				{ID: 2, TTL: 60, GrantedTTL: 60, RemainingTTL: 55},
			}, nil
		},
	}

	svc := NewLeaseService(host, 1)
	leases, err := svc.Leases(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(leases) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(leases))
	}
	if leases[0].ID != 1 {
		t.Fatalf("expected first lease ID 1, got %d", leases[0].ID)
	}
	if leases[1].ID != 2 {
		t.Fatalf("expected second lease ID 2, got %d", leases[1].ID)
	}
}

func TestLeaseService_Leases_ReadError(t *testing.T) {
	sentinel := errors.New("shard unavailable")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.Leases(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

func TestLeaseService_Leases_Empty(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []*Lease{}, nil
		},
	}

	svc := NewLeaseService(host, 1)
	leases, err := svc.Leases(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(leases) != 0 {
		t.Fatalf("expected 0 leases, got %d", len(leases))
	}
}

func TestLeaseService_Leases_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not-a-slice", nil
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.Leases(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var urte *unexpectedResultTypeError
	if !errors.As(err, &urte) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// translateProposeError
// ---------------------------------------------------------------------------

func TestLeaseService_translateProposeError_NotLeader(t *testing.T) {
	err := translateProposeError(&quicraft.NotLeaderError{
		ShardID:       10,
		LeaderID:      2,
		LeaderAddress: "192.168.0.2:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 10 {
		t.Fatalf("expected ShardID 10, got %d", nle.ShardID)
	}
}

func TestLeaseService_translateProposeError_GenericError(t *testing.T) {
	sentinel := errors.New("generic failure")

	err := translateProposeError(sentinel)

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// translateReadError
// ---------------------------------------------------------------------------

func TestLeaseService_translateReadError_NotLeader(t *testing.T) {
	svc := &LeaseService{host: &mockHost{}, shardID: 1}

	err := svc.translateReadError(&quicraft.NotLeaderError{
		ShardID:       12,
		LeaderID:      6,
		LeaderAddress: "192.168.0.6:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 12 {
		t.Fatalf("expected ShardID 12, got %d", nle.ShardID)
	}
}

func TestLeaseService_translateReadError_GenericError(t *testing.T) {
	svc := &LeaseService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("read failure")

	err := svc.translateReadError(sentinel)

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

// TestLeaseService_translateReadError_LeaseNotFound pins that a missing
// lease is returned unwrapped, the same way MVCCKVService.translateReadError
// returns KeyNotFoundError unwrapped rather than folding it into ReadError.
// Every transport's classifier already had a *LeaseNotFoundError arm; before
// this, translateReadError buried it in *ReadError instead, and gRPC, Unix,
// QUIC and MCP all reported a missing lease as an internal server error
// (go-qrdb#387).
func TestLeaseService_translateReadError_LeaseNotFound(t *testing.T) {
	svc := &LeaseService{host: &mockHost{}, shardID: 1}

	err := svc.translateReadError(&LeaseNotFoundError{LeaseID: 42})

	var lnf *LeaseNotFoundError
	if !errors.As(err, &lnf) {
		t.Fatalf("expected *LeaseNotFoundError, got %T: %v", err, err)
	}
	if lnf.LeaseID != 42 {
		t.Fatalf("expected LeaseID 42, got %d", lnf.LeaseID)
	}
	var re *ReadError
	if errors.As(err, &re) {
		t.Fatalf("expected a bare *LeaseNotFoundError, not one wrapped in *ReadError: %v", err)
	}
}
