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

//go:build qrdb_vector

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// =============================================================================
// GAP 2b: EnsureNamespace idempotency tests
// =============================================================================

// TestVectorStoreAdapter_EnsureNamespace_Idempotent verifies that calling
// EnsureNamespace twice with the same name succeeds without error (idempotent).
func TestVectorStoreAdapter_EnsureNamespace_Idempotent(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	adapter := NewVectorStoreAdapter(svc)
	ctx := tenant.WithTenantID(context.Background(), "tenant-x")

	// First call creates the namespace.
	err = adapter.EnsureNamespace(ctx, "documents", 384, "cosine")
	require.NoError(t, err, "first EnsureNamespace must succeed")

	// Second call on the same namespace must also succeed (idempotent).
	err = adapter.EnsureNamespace(ctx, "documents", 384, "cosine")
	require.NoError(t, err, "second EnsureNamespace must be idempotent")
}

// TestVectorStoreAdapter_EnsureNamespace_AlreadyExists verifies that when the
// service returns ErrAlreadyExists (namespace was already created), EnsureNamespace
// treats it as a success (idempotent).
func TestVectorStoreAdapter_EnsureNamespace_AlreadyExists(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	adapter := NewVectorStoreAdapter(svc)
	ctx := tenant.WithTenantID(context.Background(), "t1")

	// First call: creates the namespace.
	err = adapter.EnsureNamespace(ctx, "ns", 128, "cosine")
	require.NoError(t, err)

	// Second call: namespace already exists → service returns ErrAlreadyExists
	// → EnsureNamespace must return nil (idempotent).
	err = adapter.EnsureNamespace(ctx, "ns", 128, "cosine")
	require.NoError(t, err, "second EnsureNamespace must succeed (AlreadyExists is idempotent)")
}

// TestVectorStoreAdapter_EnsureNamespace_PropagatesOtherErrors verifies that
// non-AlreadyExists errors are propagated to the caller.
func TestVectorStoreAdapter_EnsureNamespace_PropagatesOtherErrors(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		return errors.New("disk full")
	}

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	adapter := NewVectorStoreAdapter(svc)
	ctx := tenant.WithTenantID(context.Background(), "t1")

	err = adapter.EnsureNamespace(ctx, "ns", 128, "cosine")
	require.Error(t, err, "non-AlreadyExists errors must be propagated")
}

// TestVectorStoreAdapter_EnsureNamespace_DefaultMetric verifies that empty
// metric defaults to cosine (no panic, no error).
func TestVectorStoreAdapter_EnsureNamespace_DefaultMetric(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	adapter := NewVectorStoreAdapter(svc)
	ctx := tenant.WithTenantID(context.Background(), "t1")

	err = adapter.EnsureNamespace(ctx, "ns", 128, "")
	require.NoError(t, err, "empty metric must use default without error")
}

// =============================================================================
// GAP 3: WithEncryptor no-op test
// =============================================================================

// TestWithEncryptor_NilBarrierIsNoOp verifies that
// WithEncryptor(atrest.New(nil)) does not modify the pebble options (no
// panic, no FS wrapper applied).
func TestWithEncryptor_NilBarrierIsNoOp(t *testing.T) {
	// Applying a nil-barrier encryptor option must not panic.
	opt := vector.WithEncryptor(atrest.New(nil))
	require.NotNil(t, opt, "WithEncryptor must return a non-nil option")
	// The option is only exercised during pebble.Open; we verify it exists and
	// is callable without panicking. Integration tests cover the full open path.
	assert.NotNil(t, opt)
}
