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
	"log/slog"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// createDeadlineWatchdog is how long a test waits for a create bounded by a
// short deadline to answer. A create that is not bounded blocks until the
// watchdog fires, which is the defect these tests pin.
const createDeadlineWatchdog = 5 * time.Second

// stallingQuotaUsage stands in for a quota-usage store whose shard has no
// leader: reserving the replica budget blocks until the caller's context
// ends, as a consensus proposal does. It records each raft-group release
// and whether the context it ran under was still live.
type stallingQuotaUsage struct {
	mu           sync.Mutex
	raftReserved int64
	releases     []error
}

func (s *stallingQuotaUsage) IncrementQuotaUsage(ctx context.Context, _ string, res quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	if res == quota.ResourceTotalReplicas {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if res == quota.ResourceRaftGroups {
		s.raftReserved += delta
	}
	return nil, nil
}

func (s *stallingQuotaUsage) DecrementQuotaUsage(ctx context.Context, _ string, res quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res == quota.ResourceRaftGroups {
		s.raftReserved -= delta
		s.releases = append(s.releases, ctx.Err())
	}
	return nil, nil
}

// stallingClearSealer is an active cluster sealer whose seal-record write
// cannot reach consensus: Clear signals entered, blocks until the caller's
// context ends, then sends the context's error on released.
type stallingClearSealer struct {
	*clustersealtest.Sealer
	entered  chan<- struct{}
	released chan<- error
}

func (s stallingClearSealer) Clear(ctx context.Context, _ clusterseal.Barrier, _ clusterseal.Principal) (clusterseal.Record, error) {
	s.entered <- struct{}{}
	<-ctx.Done()
	s.released <- ctx.Err()
	return clusterseal.Record{}, ctx.Err()
}

// newDeadlineRaftTenantService builds a raft-tenant DatabaseService whose
// replica reservation stalls, bounded by timeout.
func newDeadlineRaftTenantService(t *testing.T, usage QuotaUsageRecorder, timeout time.Duration) DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	info := &tenant.TenantInfo{ID: "racme", IsolationType: tenant.IsolationRaft, VolumePool: "default"}
	return NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithTenantDataRootResolver(&fakeDataRootResolver{roots: map[string]string{info.ID: t.TempDir()}}),
		WithQuotaUsageRecorder(usage),
		WithDatabaseCreateTimeout(timeout),
	)
}

// createWithin runs create and fails the test if it does not answer within
// createDeadlineWatchdog.
func createWithin(t *testing.T, create func() (*database.DatabaseInfo, error)) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := create()
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(createDeadlineWatchdog):
		t.Fatalf("create did not answer within %v: it is not bounded by the service's create deadline", createDeadlineWatchdog)
		return nil
	}
}

func TestCreateForTenant_StalledConsensus_AnswersWithTimeoutError(t *testing.T) {
	usage := &stallingQuotaUsage{}
	svc := newDeadlineRaftTenantService(t, usage, 50*time.Millisecond)
	ctx := tenant.WithTenantID(context.Background(), "racme")

	err := createWithin(t, func() (*database.DatabaseInfo, error) {
		return svc.CreateForTenant(ctx, testDBConfig("app"))
	})

	var timeoutErr *DatabaseCreateTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v (%T), want *DatabaseCreateTimeoutError", err, err)
	}
	if timeoutErr.Name != "app" || timeoutErr.Timeout != 50*time.Millisecond {
		t.Errorf("timeout error = %+v, want Name app, Timeout 50ms", timeoutErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap the step's context.DeadlineExceeded", err)
	}
}

func TestCreateForTenant_StalledConsensus_ReleasesReservationsUnderLiveContext(t *testing.T) {
	usage := &stallingQuotaUsage{}
	svc := newDeadlineRaftTenantService(t, usage, 50*time.Millisecond)
	ctx := tenant.WithTenantID(context.Background(), "racme")

	if err := createWithin(t, func() (*database.DatabaseInfo, error) {
		return svc.CreateForTenant(ctx, testDBConfig("app"))
	}); err == nil {
		t.Fatal("create succeeded, want the deadline to end it")
	}

	usage.mu.Lock()
	defer usage.mu.Unlock()
	if usage.raftReserved != 0 {
		t.Errorf("raft-group usage after the failed create = %d, want 0 (reservation released)", usage.raftReserved)
	}
	if len(usage.releases) != 1 || usage.releases[0] != nil {
		t.Errorf("release contexts = %v, want one release under a live context", usage.releases)
	}
}

func TestCreateForTenant_CallerCancelled_ReturnsCallerError(t *testing.T) {
	usage := &stallingQuotaUsage{}
	svc := newDeadlineRaftTenantService(t, usage, time.Minute)
	ctx, cancel := context.WithCancel(tenant.WithTenantID(context.Background(), "racme"))
	time.AfterFunc(20*time.Millisecond, cancel)

	err := createWithin(t, func() (*database.DatabaseInfo, error) {
		return svc.CreateForTenant(ctx, testDBConfig("app"))
	})

	var timeoutErr *DatabaseCreateTimeoutError
	if errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want the caller's own cancellation, not the service deadline", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestCreateWithCredentials_StalledSealRecord_RollsBackAndAnswers pins that
// the service's create deadline releases a stalled seal-record write, and
// that the create then rolls back and answers. The deadline covers the whole
// create, the manager's catalog-record write included: that write is a
// consensus step, which is what the deadline exists to bound. The test runs
// in a synctest bubble, so its fake clock advances only while every
// goroutine in it is durably blocked: the manager's disk and catalog work
// before the seal-record write takes no time on the deadline's clock however
// loaded the host is, and the deadline fires only once the stalled write is
// all that waits. The rollback runs under database.DropRollbackContext, and
// its file and directory fsyncs likewise take no fake time.
func TestCreateWithCredentials_StalledSealRecord_RollsBackAndAnswers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
		mgr := databasetest.NewManager(logger, t.TempDir())
		t.Cleanup(func() { _ = mgr.Close() })
		entered := make(chan struct{}, 1)
		released := make(chan error, 1)
		svc := NewDatabaseService(mgr,
			WithDatabaseClusterSealer(stallingClearSealer{Sealer: clustersealtest.New(true), entered: entered, released: released}),
			WithDatabaseCreateTimeout(50*time.Millisecond),
		)
		dc, ok := svc.(DatabaseCredentialCreator)
		if !ok {
			t.Fatalf("%T is not a DatabaseCredentialCreator", svc)
		}

		done := make(chan error, 1)
		go func() {
			_, err := dc.CreateWithCredentials(context.Background(), testDBConfig("app"), seal.Credentials{})
			done <- err
		}()

		// The manager's create runs first, on the deadline's clock; its disk
		// and catalog work never blocks durably, so the bubble's clock stands
		// still until the seal-record write is all that is left waiting.
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("create answered %v before reaching the seal-record write", err)
		case <-time.After(createDeadlineWatchdog):
			t.Fatalf("create did not reach the seal-record write within %v", createDeadlineWatchdog)
		}
		select {
		case err := <-released:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("seal-record write released by %v, want the create deadline's context.DeadlineExceeded", err)
			}
		case <-time.After(createDeadlineWatchdog):
			t.Fatalf("seal-record write not released within %v: it is not bounded by the service's create deadline", createDeadlineWatchdog)
		}

		var err error
		select {
		case err = <-done:
		case <-time.After(database.DropRollbackTimeout + createDeadlineWatchdog):
			t.Fatalf("create did not answer within the rollback bound %v after its deadline", database.DropRollbackTimeout)
		}
		var timeoutErr *DatabaseCreateTimeoutError
		if !errors.As(err, &timeoutErr) {
			t.Fatalf("err = %v (%T), want *DatabaseCreateTimeoutError", err, err)
		}
		if _, gerr := mgr.Get("app"); gerr == nil {
			t.Error("database app still exists after its create timed out, want it rolled back")
		}
	})
}

func TestWithDatabaseCreateTimeout_DefaultsAndIgnoresNonPositive(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	for _, tc := range []struct {
		name string
		opts []DatabaseServiceOption
		want time.Duration
	}{
		{"default", nil, DefaultDatabaseCreateTimeout},
		{"override", []DatabaseServiceOption{WithDatabaseCreateTimeout(3 * time.Second)}, 3 * time.Second},
		{"zero ignored", []DatabaseServiceOption{WithDatabaseCreateTimeout(0)}, DefaultDatabaseCreateTimeout},
		{"negative ignored", []DatabaseServiceOption{WithDatabaseCreateTimeout(-time.Second)}, DefaultDatabaseCreateTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewDatabaseService(mgr, tc.opts...).(*DatabaseService)
			if svc.createTimeout != tc.want {
				t.Fatalf("createTimeout = %v, want %v", svc.createTimeout, tc.want)
			}
		})
	}
}
