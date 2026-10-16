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

package secret_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secret"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// fakeRetriever implements CredentialRetriever for unit tests.
type fakeRetriever struct {
	fn     func(ctx context.Context, name string) ([]byte, error)
	called bool
}

func (f *fakeRetriever) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	f.called = true
	v, err := f.fn(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(v)
}

// mustBuffer returns a protected buffer holding s.
func mustBuffer(t *testing.T, s string) *secure.Buffer {
	t.Helper()
	buf, err := secure.NewBufferFromBytes([]byte(s))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	return buf
}

// bufferString reads buf's value and destroys it.
func bufferString(t *testing.T, buf *secure.Buffer) string {
	t.Helper()
	defer buf.Destroy()
	var s string
	if err := buf.WithBytes(func(b []byte) error {
		s = string(b)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	return s
}

func testDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------- CredentialResolver ----------

func TestCredentialResolver_Resolves(t *testing.T) {
	retrieve := func(_ context.Context, name string) (*secure.Buffer, error) {
		if name == "db-password" {
			return mustBuffer(t, "super-secret"), nil
		}
		return nil, errors.New("not found")
	}
	res := secret.NewCredentialResolver(retrieve)

	sv, err := res.Resolve(context.Background(), "credential://db-password")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plain := plaintextOf(t, sv); plain != "super-secret" {
		t.Errorf("got %q, want super-secret", plain)
	}
	if sv.Strategy != secret.StrategyCredential {
		t.Errorf("strategy = %s, want credential", sv.Strategy)
	}
}

func TestCredentialResolver_EmptyName(t *testing.T) {
	res := secret.NewCredentialResolver(func(_ context.Context, _ string) (*secure.Buffer, error) {
		return mustBuffer(t, "v"), nil
	})

	_, err := res.Resolve(context.Background(), "credential://")
	var ive *secret.InvalidSecretURIError
	if !errors.As(err, &ive) {
		t.Fatalf("expected InvalidSecretURIError, got %T: %v", err, err)
	}
}

func TestCredentialResolver_RetrieveError(t *testing.T) {
	backendErr := errors.New("vault sealed")
	res := secret.NewCredentialResolver(func(_ context.Context, _ string) (*secure.Buffer, error) {
		return nil, backendErr
	})

	_, err := res.Resolve(context.Background(), "credential://db-password")
	var rfe *secret.ResolutionFailedError
	if !errors.As(err, &rfe) {
		t.Fatalf("expected ResolutionFailedError, got %T: %v", err, err)
	}
	if rfe.Scheme != "credential" {
		t.Errorf("scheme = %q, want credential", rfe.Scheme)
	}
	if !errors.Is(err, backendErr) {
		t.Error("Unwrap chain must reach the underlying backend error")
	}
}

func TestCredentialResolver_NotFound(t *testing.T) {
	res := secret.NewCredentialResolver(func(_ context.Context, _ string) (*secure.Buffer, error) {
		return nil, credentials.ErrCredentialNotFound
	})

	_, err := res.Resolve(context.Background(), "credential://missing")
	var nfe *secret.SecretNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected SecretNotFoundError, got %T: %v", err, err)
	}
	if nfe.Scheme != "credential" || nfe.Name != "missing" {
		t.Errorf("SecretNotFoundError = %+v, want scheme=credential name=missing", nfe)
	}
}

func TestCredentialResolver_NilCtx(t *testing.T) {
	res := secret.NewCredentialResolver(func(_ context.Context, _ string) (*secure.Buffer, error) {
		return mustBuffer(t, "v"), nil
	})
	_, err := res.Resolve(nil, "credential://x") //nolint:staticcheck
	if !errors.Is(err, secret.ErrNilContext) {
		t.Errorf("expected ErrNilContext, got %v", err)
	}
}

func TestCredentialResolver_CanResolve(t *testing.T) {
	res := secret.NewCredentialResolver(nil)
	if !res.CanResolve("credential://x") {
		t.Error("expected CanResolve=true for credential:// prefix")
	}
	if res.CanResolve("kms://x") {
		t.Error("expected CanResolve=false for non-credential scheme")
	}
	if res.CanResolve("plain-value") {
		t.Error("expected CanResolve=false for plain value")
	}
}

func TestCredentialResolver_NilRetrieve(t *testing.T) {
	res := secret.NewCredentialResolver(nil)
	_, err := res.Resolve(context.Background(), "credential://db-password")
	var rfe *secret.ResolutionFailedError
	if !errors.As(err, &rfe) {
		t.Fatalf("nil retrieve must return ResolutionFailedError, got %T: %v", err, err)
	}
}

func TestCredentialResolverFromServicer(t *testing.T) {
	svc := &fakeRetriever{
		fn: func(_ context.Context, name string) ([]byte, error) {
			return []byte("val-" + name), nil
		},
	}
	res := secret.CredentialResolverFromServicer(svc)

	sv, err := res.Resolve(context.Background(), "credential://mykey")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plain := plaintextOf(t, sv); plain != "val-mykey" {
		t.Errorf("got %q, want val-mykey", plain)
	}
}

// ---------- Tenant-scoped routing ----------

func TestNewTenantScopedCredentialRetrieve_NoTenantContext(t *testing.T) {
	// When tenantIDFromCtx returns false, the systemSvc must be used.
	systemSvc := &fakeRetriever{
		fn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("system-val"), nil
		},
	}
	noTenant := func(_ context.Context) (string, bool) { return "", false }

	retrieve := secret.NewTenantScopedCredentialRetrieve(nil, systemSvc, noTenant, testDiscardLogger())

	val, err := retrieve(context.Background(), "db-password")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if got := bufferString(t, val); got != "system-val" {
		t.Errorf("got %q, want system-val", got)
	}
	if !systemSvc.called {
		t.Error("systemSvc must be called when no tenant context is present")
	}
}

func TestNewTenantScopedCredentialRetrieve_NilTenantIDFunc(t *testing.T) {
	// A nil tenantIDFromCtx must fall back to systemSvc.
	systemSvc := &fakeRetriever{
		fn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("system-val"), nil
		},
	}

	retrieve := secret.NewTenantScopedCredentialRetrieve(nil, systemSvc, nil, testDiscardLogger())

	val, err := retrieve(context.Background(), "key")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if got := bufferString(t, val); got != "system-val" {
		t.Errorf("got %q, want system-val", got)
	}
}

func TestNewTenantScopedCredentialRetrieve_NilRegistry(t *testing.T) {
	// Even with a non-nil tenantIDFromCtx, a nil registry must fall back.
	systemSvc := &fakeRetriever{
		fn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("system-val"), nil
		},
	}
	hasTenant := func(_ context.Context) (string, bool) { return "tenant-1", true }

	retrieve := secret.NewTenantScopedCredentialRetrieve(
		nil, // nil registry → always system path
		systemSvc,
		hasTenant,
		testDiscardLogger(),
	)

	val, err := retrieve(context.Background(), "key")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if got := bufferString(t, val); got != "system-val" {
		t.Errorf("got %q, want system-val", got)
	}
}

func TestNewTenantScopedCredentialRetrieve_TenantPath(t *testing.T) {
	logger := testDiscardLogger()
	ctx := context.Background()

	// Set up system barrier.
	sysBackend := seal.NewMemoryBackend()
	sysStrategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("sys-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sysBarrier, err := seal.NewStorageBarrier(logger, sysBackend, seal.BarrierConfig{
		RootKeyPath: "system/root",
	}, sysStrategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sysBarrier.Initialize(ctx, seal.MustCredentials(context.Background(), []byte("sys-pass"))); err != nil {
		t.Fatalf("Initialize system: %v", err)
	}

	// Set up registry.
	registry, err := seal.NewBarrierRegistry(logger, sysBarrier)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// Register tenant barrier.
	tenantBackend := seal.NewMemoryBackend()
	tenantStrategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("tenant-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy (tenant): %v", err)
	}
	tb, err := registry.RegisterTenant(ctx, "tenant-1", tenantBackend, seal.BarrierConfig{}, tenantStrategy)
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	// Initialize makes the barrier unsealed.
	if err := tb.Barrier().Initialize(ctx, seal.MustCredentials(context.Background(), []byte("tenant-pass"))); err != nil {
		t.Fatalf("Initialize tenant: %v", err)
	}

	// Pre-store a credential directly in the tenant barrier.
	if err := tb.Barrier().Put(ctx, "credentials/db-password", []byte("tenant-secret")); err != nil {
		t.Fatalf("Put credential: %v", err)
	}

	systemSvc := &fakeRetriever{
		fn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("should-not-be-called"), nil
		},
	}
	tenantIDFromCtx := func(_ context.Context) (string, bool) { return "tenant-1", true }

	retrieve := secret.NewTenantScopedCredentialRetrieve(registry, systemSvc, tenantIDFromCtx, logger)

	val, err := retrieve(ctx, "db-password")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if got := bufferString(t, val); got != "tenant-secret" {
		t.Errorf("got %q, want tenant-secret", got)
	}
	if systemSvc.called {
		t.Error("systemSvc must NOT be called when tenant context is present")
	}
}

func TestNewTenantScopedCredentialRetrieve_UnknownTenant(t *testing.T) {
	logger := testDiscardLogger()
	ctx := context.Background()

	sysBackend := seal.NewMemoryBackend()
	sysStrategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("sys-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sysBarrier, err := seal.NewStorageBarrier(logger, sysBackend, seal.BarrierConfig{
		RootKeyPath: "system/root",
	}, sysStrategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sysBarrier.Initialize(ctx, seal.MustCredentials(context.Background(), []byte("sys-pass"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	registry, err := seal.NewBarrierRegistry(logger, sysBarrier)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	systemSvc := &fakeRetriever{
		fn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("system-val"), nil
		},
	}
	// Request a tenant that has not been registered.
	tenantIDFromCtx := func(_ context.Context) (string, bool) { return "ghost-tenant", true }

	retrieve := secret.NewTenantScopedCredentialRetrieve(registry, systemSvc, tenantIDFromCtx, logger)

	_, err = retrieve(ctx, "key")
	if err == nil {
		t.Error("expected an error for an unknown tenant")
	}
}

// ---------- Protected-memory hand-off ----------

func TestNewSecretValueFromBuffer_SealsAndConsumesBuffer(t *testing.T) {
	buf := mustBuffer(t, "hunter2")

	sv, err := secret.NewSecretValueFromBuffer(context.Background(), "credential://x", buf, secret.StrategyCredential)
	if err != nil {
		t.Fatalf("NewSecretValueFromBuffer: %v", err)
	}
	defer sv.Clear()

	if got := plaintextOf(t, sv); got != "hunter2" {
		t.Errorf("plaintext = %q, want hunter2", got)
	}
	if sv.Strategy != secret.StrategyCredential {
		t.Errorf("strategy = %s, want credential", sv.Strategy)
	}
	if err := buf.WithBytes(func([]byte) error { return nil }); err == nil {
		t.Error("the source buffer must be consumed (destroyed) by NewSecretValueFromBuffer")
	}
}

func TestNewSecretValueFromBuffer_NilBufferFailsClosed(t *testing.T) {
	sv, err := secret.NewSecretValueFromBuffer(context.Background(), "credential://x", nil, secret.StrategyCredential)
	var sfe *secret.SealFailedError
	if !errors.As(err, &sfe) {
		t.Fatalf("expected SealFailedError, got %T: %v", err, err)
	}
	if sv != nil {
		t.Error("a failed seal must not return a SecretValue")
	}
}

func TestCredentialResolver_DestroysRetrievedBuffer(t *testing.T) {
	var retrieved *secure.Buffer
	res := secret.NewCredentialResolver(func(_ context.Context, _ string) (*secure.Buffer, error) {
		retrieved = mustBuffer(t, "once")
		return retrieved, nil
	})

	sv, err := res.Resolve(context.Background(), "credential://k")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer sv.Clear()

	if got := plaintextOf(t, sv); got != "once" {
		t.Errorf("plaintext = %q, want once", got)
	}
	if err := retrieved.WithBytes(func([]byte) error { return nil }); err == nil {
		t.Error("Resolve must destroy the buffer the retrieve function returned")
	}
}

func TestCredentialResolver_NilBufferWithoutErrorFailsClosed(t *testing.T) {
	res := secret.NewCredentialResolver(func(_ context.Context, _ string) (*secure.Buffer, error) {
		return nil, nil
	})

	_, err := res.Resolve(context.Background(), "credential://k")
	var rfe *secret.ResolutionFailedError
	if !errors.As(err, &rfe) {
		t.Fatalf("expected ResolutionFailedError, got %T: %v", err, err)
	}
}
