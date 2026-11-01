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
