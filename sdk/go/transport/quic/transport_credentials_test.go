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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newClosedCredentialTransport returns a Transport that has been closed,
// so every method short-circuits to *transport.ClosedError.
func newClosedCredentialTransport(t *testing.T) *Transport {
	t.Helper()
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
	return tr
}

// newOpenCredentialTransport returns a Transport that has not been closed
// but has no live QUIC connection. Nil-request guards run before any
// network I/O, so this is sufficient to exercise the validation paths.
func newOpenCredentialTransport(t *testing.T) *Transport {
	t.Helper()
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// ---------------------------------------------------------------------------
// Closed-state behavior
// ---------------------------------------------------------------------------

func TestTransport_CredentialRetrieveVersion_Closed(t *testing.T) {
	tr := newClosedCredentialTransport(t)
	_, err := tr.retrieveVersionResponse(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "k", Version: 1})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Nil-request guards
//
// Methods that take a request pointer must reject nil with a typed
// nilCredentialRequestError wrapped in a transport.RequestError. The
// transport is open but unconnected; the guard runs before any I/O.
// ---------------------------------------------------------------------------

func TestTransport_CredentialSubmit_NilRequest(t *testing.T) {
	tr := newOpenCredentialTransport(t)
	err := tr.CredentialSubmit(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var nre *nilCredentialRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T: %v", err, err)
	}
	if nre.Method != "CredentialSubmit" {
		t.Errorf("Method = %q, want CredentialSubmit", nre.Method)
	}
}

func TestTransport_CredentialRotate_NilRequest(t *testing.T) {
	tr := newOpenCredentialTransport(t)
	_, err := tr.CredentialRotate(context.Background(), nil)
	var nre *nilCredentialRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T", err)
	}
}

func TestTransport_CredentialSetRotationPolicy_ClosedAndNilRequest(t *testing.T) {
	var ce *transport.ClosedError
	if err := newClosedCredentialTransport(t).CredentialSetRotationPolicy(context.Background(), &transport.CredentialSetRotationPolicyRequest{Name: "k"}); !errors.As(err, &ce) {
		t.Fatalf("expected *ClosedError, got %T", err)
	}
	var nre *nilCredentialRequestError
	if err := newOpenCredentialTransport(t).CredentialSetRotationPolicy(context.Background(), nil); !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T", err)
	}
}

func TestTransport_CredentialRotationPolicy_Closed(t *testing.T) {
	var ce *transport.ClosedError
	if _, err := newClosedCredentialTransport(t).CredentialRotationPolicy(context.Background(), "k"); !errors.As(err, &ce) {
		t.Fatalf("expected *ClosedError, got %T", err)
	}
}

func TestTransport_CredentialMigrationConflicts_Closed(t *testing.T) {
	var ce *transport.ClosedError
	if _, err := newClosedCredentialTransport(t).CredentialMigrationConflicts(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("expected *ClosedError, got %T", err)
	}
}

func TestTransport_CredentialResolveMigrationConflict_ClosedAndNilRequest(t *testing.T) {
	var ce *transport.ClosedError
	req := &transport.CredentialResolveMigrationConflictRequest{Name: "k", Resolution: "import"}
	if err := newClosedCredentialTransport(t).CredentialResolveMigrationConflict(context.Background(), req); !errors.As(err, &ce) {
		t.Fatalf("expected *ClosedError, got %T", err)
	}
	var nre *nilCredentialRequestError
	if err := newOpenCredentialTransport(t).CredentialResolveMigrationConflict(context.Background(), nil); !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T", err)
	}
}

func TestTransport_CredentialRetrieveVersion_NilRequest(t *testing.T) {
	tr := newOpenCredentialTransport(t)
	_, err := tr.retrieveVersionResponse(context.Background(), nil)
	var nre *nilCredentialRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T", err)
	}
}

func TestTransport_CredentialListVersions_NilRequest(t *testing.T) {
	tr := newOpenCredentialTransport(t)
	_, err := tr.CredentialListVersions(context.Background(), nil)
	var nre *nilCredentialRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T", err)
	}
}

func TestTransport_CredentialGCExpired_NilRequest(t *testing.T) {
	tr := newOpenCredentialTransport(t)
	_, err := tr.CredentialGCExpired(context.Background(), nil)
	var nre *nilCredentialRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// nilCredentialRequestError
// ---------------------------------------------------------------------------

func TestNilCredentialRequestError_Error(t *testing.T) {
	e := &nilCredentialRequestError{Method: "CredentialRotate"}
	got := e.Error()
	if got == "" {
		t.Error("expected non-empty error message")
	}
	if want := "transport: CredentialRotate: nil request"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Interface conformance
// ---------------------------------------------------------------------------

// TestTransport_ImplementsCredentialClient is enforced by the compile-time
// var _ transport.CredentialClient = (*Transport)(nil) assertion in
// transport_credentials.go, but a runtime test guards against accidental
// removal of that assertion line during refactors.
func TestTransport_ImplementsCredentialClient(t *testing.T) {
	tr := newOpenCredentialTransport(t)
	var _ transport.CredentialClient = tr
}
