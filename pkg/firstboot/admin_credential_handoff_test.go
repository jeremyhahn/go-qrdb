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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestResolveAdminCertForFirstBoot_NilStore covers the no-store case: a
// composer with nothing to resolve gets (nil, nil) regardless of policy.
func TestResolveAdminCertForFirstBoot_NilStore(t *testing.T) {
	got, err := ResolveAdminCertForFirstBoot(context.Background(), nil, noAdminLookup{}, auth.MTLSBindingRequired, time.Second)
	if err != nil || got != nil {
		t.Fatalf("ResolveAdminCertForFirstBoot(nil store) = (%v, %v), want (nil, nil)", got, err)
	}
}

// TestResolveAdminCertForFirstBoot_RequiredDelegatesToAwait covers the
// "required" policy: it is AwaitPendingAdminCert, bounded wait included.
func TestResolveAdminCertForFirstBoot_RequiredDelegatesToAwait(t *testing.T) {
	store := newMemPendingAdminCertStore()
	if err := WritePendingAdminCert(context.Background(), store, []byte("cert-pem")); err != nil {
		t.Fatalf("WritePendingAdminCert: %v", err)
	}
	got, err := ResolveAdminCertForFirstBoot(context.Background(), store, noAdminLookup{}, auth.MTLSBindingRequired, time.Second)
	if err != nil {
		t.Fatalf("ResolveAdminCertForFirstBoot: %v", err)
	}
	if string(got) != "cert-pem" {
		t.Fatalf("got %q, want cert-pem", got)
	}
}

// TestResolveAdminCertForFirstBoot_RequiredTimesOut covers the "required"
// policy's bounded-wait failure, surfaced unchanged.
func TestResolveAdminCertForFirstBoot_RequiredTimesOut(t *testing.T) {
	_, err := ResolveAdminCertForFirstBoot(context.Background(), newMemPendingAdminCertStore(), noAdminLookup{}, auth.MTLSBindingRequired, 10*time.Millisecond)
	var noCred *NoPendingAdminCredentialError
	if !errors.As(err, &noCred) {
		t.Fatalf("err = %T(%v), want *NoPendingAdminCredentialError", err, err)
	}
}

// TestResolveAdminCertForFirstBoot_OpportunisticRead covers any other
// policy: a single, non-blocking store.Get, present or absent.
func TestResolveAdminCertForFirstBoot_OpportunisticRead(t *testing.T) {
	store := newMemPendingAdminCertStore()
	got, err := ResolveAdminCertForFirstBoot(context.Background(), store, noAdminLookup{}, auth.MTLSBindingEnrolled, time.Second)
	if err != nil || got != nil {
		t.Fatalf("absent record: got (%v, %v), want (nil, nil)", got, err)
	}

	if err := WritePendingAdminCert(context.Background(), store, []byte("cert-pem")); err != nil {
		t.Fatalf("WritePendingAdminCert: %v", err)
	}
	got, err = ResolveAdminCertForFirstBoot(context.Background(), store, noAdminLookup{}, auth.MTLSBindingEnrolled, time.Second)
	if err != nil {
		t.Fatalf("ResolveAdminCertForFirstBoot: %v", err)
	}
	if string(got) != "cert-pem" {
		t.Fatalf("got %q, want cert-pem", got)
	}
}

// TestResolveAdminCertForFirstBoot_OpportunisticReadNeverReturnsTheConsumedMarker
// is the regression test for go-qrdb#560 review B2: the opportunistic,
// non-blocking read (any policy other than "required") handed the
// pending-admin-cert record back VERBATIM, with no check for
// ClearPendingAdminCert's own "consumed" marker -- a composer calling this
// after a ceremony's credential had already been bound and cleared would
// receive the literal bytes "consumed" and treat them as a certificate to
// bind. AwaitPendingAdminCert's bounded-wait branch already guards against
// exactly this; this proves the opportunistic branch does too.
func TestResolveAdminCertForFirstBoot_OpportunisticReadNeverReturnsTheConsumedMarker(t *testing.T) {
	store := newMemPendingAdminCertStore()
	if err := WritePendingAdminCert(context.Background(), store, []byte("cert-pem")); err != nil {
		t.Fatalf("WritePendingAdminCert: %v", err)
	}
	if err := ClearPendingAdminCert(context.Background(), store); err != nil {
		t.Fatalf("ClearPendingAdminCert: %v", err)
	}

	got, err := ResolveAdminCertForFirstBoot(context.Background(), store, noAdminLookup{}, auth.MTLSBindingEnrolled, time.Second)
	if err != nil {
		t.Fatalf("ResolveAdminCertForFirstBoot: %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveAdminCertForFirstBoot returned the consumed marker as a certificate: %q", got)
	}
}

// TestAdminCertPoolFromCABundle covers the empty, malformed and valid cases.
func TestAdminCertPoolFromCABundle(t *testing.T) {
	if pool := AdminCertPoolFromCABundle(nil); pool != nil {
		t.Fatalf("empty bundle: got %v, want nil", pool)
	}
	if pool := AdminCertPoolFromCABundle([]byte("not a certificate")); pool != nil {
		t.Fatalf("malformed bundle: got %v, want nil", pool)
	}

	pool := AdminCertPoolFromCABundle(realCertPEM(t))
	if pool == nil {
		t.Fatal("valid bundle: got nil pool")
	}
}
