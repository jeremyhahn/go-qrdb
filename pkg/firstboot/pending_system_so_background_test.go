// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package firstboot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// blockingPINChecker holds SOPINCommitted until release closes, the way a
// linearizable read holds on a node whose cluster has no quorum yet.
type blockingPINChecker struct {
	entered chan struct{}
	release chan struct{}
	inner   SOPINChecker
}

func (b *blockingPINChecker) SOPINCommitted(ctx context.Context, tenantID string) (bool, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return b.inner.SOPINCommitted(ctx, tenantID)
}

func (b *blockingPINChecker) SOPINRecordMatches(ctx context.Context, tenantID, record string) (bool, error) {
	return b.inner.SOPINRecordMatches(ctx, tenantID, record)
}

func TestClearPendingSystemSOInBackground_DoesNotBlockTheCallerOnTheSOPINRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newMemPendingAdminCertStore()
	users := newSOUsers()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)
	bindPendingSO(t, users, so)

	pins := &blockingPINChecker{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		inner:   scriptedPINChecker{committed: true, matches: true},
	}
	returned := make(chan (<-chan struct{}), 1)
	go func() {
		returned <- ClearPendingSystemSOInBackground(ctx, store, users, pins, time.Millisecond, 3, discardLogger())
	}()

	var done <-chan struct{}
	select {
	case done = <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the caller blocked on the SO PIN read")
	}
	select {
	case <-pins.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the background check never ran")
	}
	pending, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	assert.NotNil(t, pending, "the record stays until the read answers")

	close(pins.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the background check never finished")
	}
	assert.True(t, SystemSOConsumed(ctx, store), "an enrolled SO's record is consumed once the read answers")
}

func TestClearPendingSystemSOInBackground_RetriesUntilTheSOIsEnrolled(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := newSOUsers()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)
	bindPendingSO(t, users, so)

	// A failing check is retried, and a bounded run that never succeeds
	// keeps the record for the next start.
	done := ClearPendingSystemSOInBackground(ctx, store, users,
		scriptedPINChecker{committedErr: errBranchFault}, time.Millisecond, 3, discardLogger())
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("bounded retries never ended")
	}
	assert.False(t, SystemSOConsumed(ctx, store))

	done = ClearPendingSystemSOInBackground(ctx, store, users,
		scriptedPINChecker{committed: true, matches: true}, time.Millisecond, 3, discardLogger())
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the background check never finished")
	}
	assert.True(t, SystemSOConsumed(ctx, store))
}

func TestClearPendingSystemSOInBackground_StopsOnUnverifiedAndCancel(t *testing.T) {
	ctx := context.Background()
	store := newMemPendingAdminCertStore()
	users := newSOUsers()
	require.NoError(t, WriteIssuedSystemSOIfAny(ctx, soCeremonyCore(t), store, nil))
	so, err := PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.NotNil(t, so)
	_, err = users.CreateUser(ctx, &user.CreateUserRequest{Username: so.Username, TenantID: tenantid.System,
		RoleIDs: []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}})
	require.NoError(t, err)
	bindPendingSO(t, users, so)

	wait := func(done <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal(what)
		}
	}

	// A committed PIN that is not the record's is never retried, even with
	// a long interval, a nil logger and a non-positive attempt count.
	wait(ClearPendingSystemSOInBackground(ctx, store, users,
		scriptedPINChecker{committed: true, matches: false}, time.Hour, 0, nil),
		"an unverified SO was retried")
	assert.False(t, SystemSOConsumed(ctx, store))

	// A cancelled context ends the wait between attempts.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	wait(ClearPendingSystemSOInBackground(cctx, store, users,
		scriptedPINChecker{committedErr: errBranchFault}, time.Hour, 5, discardLogger()),
		"a cancelled context did not stop the retries")
	assert.False(t, SystemSOConsumed(ctx, store))
}

func TestSystemSOHandoffPendingError_Error(t *testing.T) {
	err := error(&SystemSOHandoffPendingError{})
	assert.Contains(t, err.Error(), "system SO record is not durable")
}
