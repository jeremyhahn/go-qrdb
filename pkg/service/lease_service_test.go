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
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
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
	if capturedReadShardID != 77 {
		t.Fatalf("expected read-back shardID 77, got %d", capturedReadShardID)
	}
}

// ---------------------------------------------------------------------------
// Grant
// ---------------------------------------------------------------------------

func TestLeaseService_Grant_Success(t *testing.T) {
	var capturedQuery LeaseTimeToLiveQuery
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 100}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(LeaseTimeToLiveQuery)
			if !ok {
				t.Fatalf("expected LeaseTimeToLiveQuery, got %T", query)
			}
			capturedQuery = q
			return &Lease{ID: q.ID, TTL: 60, GrantedTTL: 60, RemainingTTL: 60}, nil
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
	// BUG 2 regression: RemainingTTL must reflect the live Lessor state,
	// not stay at its zero value.
	if lease.RemainingTTL != 60 {
		t.Fatalf("expected RemainingTTL 60, got %d", lease.RemainingTTL)
	}
	// Grant must read back the lease it just created, by the ID the SM
	// assigned (result.Value), not some other ID.
	if capturedQuery.ID != 100 {
		t.Fatalf("expected TimeToLive read-back for lease ID 100, got %d", capturedQuery.ID)
	}
}

// TestLeaseService_Grant_PopulatesRemainingTTLFromReadBack pins the exact
// fix for BUG 2: RemainingTTL comes from the Lessor's authoritative
// read-back (via the same path KeepAlive uses), not from an assumption
// that it always equals the client-requested ttl. Here the read-back
// reports a RemainingTTL lower than the requested ttl (as would happen if
// some time elapsed between grant and read-back), and Grant must surface
// that real value rather than the originally requested one.
func TestLeaseService_Grant_PopulatesRemainingTTLFromReadBack(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 7}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &Lease{ID: 7, TTL: 60, GrantedTTL: 60, RemainingTTL: 59}, nil
		},
	}

	svc := NewLeaseService(host, 1)
	lease, err := svc.Grant(context.Background(), 60)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if lease.RemainingTTL != 59 {
		t.Fatalf("expected RemainingTTL 59 (from read-back, not GrantedTTL), got %d", lease.RemainingTTL)
	}
}

// TestLeaseService_Grant_ReadBackError verifies that a failure in the
// post-grant TimeToLive read-back is surfaced (not masked as a successful
// grant with a zero-value lease).
func TestLeaseService_Grant_ReadBackError(t *testing.T) {
	sentinel := errors.New("read failed")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewLeaseService(host, 1)
	lease, err := svc.Grant(context.Background(), 60)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if lease != nil {
		t.Fatalf("expected nil lease on error, got %+v", lease)
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
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

func TestLeaseService_KeepAlive_Success(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 42}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(LeaseTimeToLiveQuery)
			if !ok {
				t.Fatalf("expected LeaseTimeToLiveQuery, got %T", query)
			}
			if q.ID != 42 {
				t.Fatalf("expected lease ID 42, got %d", q.ID)
			}
			return &Lease{
				ID:           42,
				TTL:          60,
				GrantedTTL:   60,
				RemainingTTL: 58,
				Keys:         [][]byte{[]byte("key1")},
			}, nil
		},
	}

	svc := NewLeaseService(host, 1)
	lease, err := svc.KeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if lease == nil {
		t.Fatal("expected non-nil lease")
	}
	if lease.ID != 42 {
		t.Fatalf("expected lease ID 42, got %d", lease.ID)
	}
	if lease.TTL != 60 {
		t.Fatalf("expected TTL 60, got %d", lease.TTL)
	}
	if lease.RemainingTTL != 58 {
		t.Fatalf("expected RemainingTTL 58, got %d", lease.RemainingTTL)
	}
}

func TestLeaseService_KeepAlive_ProposeError(t *testing.T) {
	sentinel := errors.New("proposal dropped")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.KeepAlive(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestLeaseService_KeepAlive_ReadError(t *testing.T) {
	sentinel := errors.New("state machine error")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 42}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewLeaseService(host, 1)
	_, err := svc.KeepAlive(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
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
	svc := &LeaseService{host: &mockHost{}, shardID: 1}

	err := svc.translateProposeError(&quicraft.NotLeaderError{
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
	svc := &LeaseService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("generic failure")

	err := svc.translateProposeError(sentinel)

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
