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
// the user family.
//
// pkg/api/quic/errors.go now maps BOTH service.LeaseNotFoundError and
// service.LeaseExpiredError to wire.StatusNotFound (go-qrdb#437), matching the
// gRPC transport, which answers codes.NotFound for both because neither
// protocol has an expired-specific sibling of not-found. Until #437 this wire
// had no not-found status at all and both fell through to StatusError -- the
// same status a transport fault carries -- so the message text was the ONLY
// thing separating "your lease is gone" from "this node broke". These tests
// pinned that arrangement and now pin its replacement.
//
// They exercise the wire contract directly via serverNotFound rather than
// standing up a service.LeaseServicer, mirroring TestE2E_LeaseRevoke_ServerErr
// and siblings in e2e_server_errors_test.go, which use the identical harness
// for the untyped case these tests are completing. The status and message the
// double sends are the ones a real server sends, pinned by
// pkg/api/quic.TestMapServiceError_EveryCondition.

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
	serverNotFound(srv, "lease.Revoke", "service: lease not found: 42")

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
	serverNotFound(srv, "lease.Revoke", "service: lease expired: 42")

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
	serverNotFound(srv, "lease.KeepAlive", "service: lease not found: 7")

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
	serverNotFound(srv, "lease.KeepAlive", "service: lease expired: 7")

	_, err := tr.LeaseKeepAlive(context.Background(), 7)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 7 {
		t.Errorf("LeaseID: got %d, want 7", expired.LeaseID)
	}
}

func TestE2E_LeaseTimeToLive_Expired(t *testing.T) {
	srv, tr := startE2EServer(t)
	serverNotFound(srv, "lease.TimeToLive", "service: lease expired: 99")

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
// companion: mapLeaseAbsent must not turn a SERVER FAULT into a lease error
// just because it arrived from a lease call. This is what would go red if the
// status gate in mapLeaseAbsent were deleted in favour of "any failure at a
// lease call site is a lease error".
//
// The gate it guards changed with go-qrdb#437. It used to be "StatusError
// carrying the words lease not found", which meant a fault and an absence
// arrived under the same status and only the prose separated them. It is now
// "StatusNotFound", so a fault like this one is excluded by the status before
// the message is looked at. Note that ANY StatusNotFound at a lease-by-id call
// site IS a lease not-found — the call site knows its subject, exactly as the
// gRPC transport's mapLeaseAbsent does with codes.NotFound — so it is the
// fault case, not a differently-worded absence, that this test pins.
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
