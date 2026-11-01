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

package credentials

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A store derived with ForBarrier emits through the parent's audit logger, so
// a tenant's credential operations land in the same compliance trail.
func TestForBarrier_SharesAuditLogger(t *testing.T) {
	parent, al := newAuditManualService(t)
	barrier := newTestBarrier(t)
	t.Cleanup(func() { _ = barrier.Close() })

	derived, err := parent.ForBarrier(context.Background(), barrier)
	require.NoError(t, err)
	require.NotSame(t, parent, derived)
	assert.Equal(t, StrategyBarrier, derived.Strategy())

	require.NoError(t, derived.StoreCredential(auditCtx(), "tenant-key", []byte("v")))

	ev, ok := al.findByAction("store")
	require.True(t, ok, "expected the derived store's audit event on the parent's logger")
	assert.Equal(t, "credential:tenant-key", ev.Resource)
}

// A credential written through the derived store lives in its barrier only:
// the parent's barrier never sees it.
func TestForBarrier_IsolatedFromParent(t *testing.T) {
	parent := newBarrierService(t)
	barrier := newTestBarrier(t)
	t.Cleanup(func() { _ = barrier.Close() })

	derived, err := parent.ForBarrier(context.Background(), barrier)
	require.NoError(t, err)
	ctx := auditCtx()
	require.NoError(t, derived.StoreCredential(ctx, "tenant-only", []byte("secret")))

	buf, err := derived.RetrieveCredentialBuffer(ctx, "tenant-only")
	require.NoError(t, err)
	equal, err := buf.EqualBytes([]byte("secret"))
	require.NoError(t, err)
	assert.True(t, equal)
	buf.Destroy()

	_, err = parent.RetrieveCredentialBuffer(ctx, "tenant-only")
	assert.ErrorIs(t, err, ErrCredentialNotFound,
		"the parent store must not see the derived store's credential")
}

// ForBarrier refuses a nil barrier with ErrNilBarrier rather than building a
// barrier-strategy store with nothing under it.
func TestForBarrier_NilBarrier(t *testing.T) {
	parent, _ := newAuditManualService(t)

	derived, err := parent.ForBarrier(context.Background(), nil)
	require.ErrorIs(t, err, ErrNilBarrier)
	assert.Nil(t, derived)
}
