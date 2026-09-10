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

// go-qrdb#169: carry the typed lease not-found/expired errors across the
// QUIC seam, completing #141 for the lease family the same way #289 did for
// the user family. pkg/api/quic/errors.go's mapServiceError has no case of
// its own for service.LeaseNotFoundError / service.LeaseExpiredError -- both
// fall through to its final `return wire.StatusError, []byte(err.Error())`,
// which already reproduces the exact "service: lease not found: <id>" /
// "service: lease expired: <id>" text a real server would send. These tests
// exercise that wire contract directly via serverErr rather than standing up
// a service.LeaseServicer, mirroring TestE2E_LeaseRevoke_ServerErr and
// siblings in e2e_server_errors_test.go, which use the identical harness for
// the untyped case these tests are completing.

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_LeaseRevoke_NotFound(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.Revoke", "service: lease not found: 42")

	err := tr.LeaseRevoke(context.Background(), 42)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", notFound.LeaseID)
	}
}

func TestE2E_LeaseRevoke_Expired(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.Revoke", "service: lease expired: 42")

	err := tr.LeaseRevoke(context.Background(), 42)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", expired.LeaseID)
	}
}

func TestE2E_LeaseKeepAlive_NotFound(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.KeepAlive", "service: lease not found: 7")

	_, err := tr.LeaseKeepAlive(context.Background(), 7)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 7 {
		t.Errorf("LeaseID: got %d, want 7", notFound.LeaseID)
	}
}

func TestE2E_LeaseKeepAlive_Expired(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.KeepAlive", "service: lease expired: 7")

	_, err := tr.LeaseKeepAlive(context.Background(), 7)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 7 {
		t.Errorf("LeaseID: got %d, want 7", expired.LeaseID)
	}
}

func TestE2E_LeaseTimeToLive_NotFound(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.TimeToLive", "service: lease not found: 99")

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 99 {
		t.Errorf("LeaseID: got %d, want 99", notFound.LeaseID)
	}
}

func TestE2E_LeaseTimeToLive_Expired(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.TimeToLive", "service: lease expired: 99")

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 99 {
		t.Errorf("LeaseID: got %d, want 99", expired.LeaseID)
	}
}

// TestE2E_LeaseTimeToLive_OtherErrNotReclassified is the mutation-check
// companion: mapLeaseAbsent must not turn an UNRELATED wire.StatusError body
// into a lease error just because it arrived from a lease call. This is what
// would go red if the substring checks in mapLeaseAbsent were deleted in
// favor of "any wire.StatusError at a lease call site is a lease error".
func TestE2E_LeaseTimeToLive_OtherErrNotReclassified(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverErr(srv, "lease.TimeToLive", "some unrelated failure")

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var notFound *transport.LeaseNotFoundError
	var expired *transport.LeaseExpiredError
	if errors.As(err, &notFound) || errors.As(err, &expired) {
		t.Fatalf("expected a non-lease error, got %T: %v", err, err)
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != int(wire.StatusError) {
		t.Errorf("StatusCode: got %d, want %d", reqErr.StatusCode, wire.StatusError)
	}
}
