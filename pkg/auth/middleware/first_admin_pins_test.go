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

package middleware

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// WithFirstAdminPins -- the trust-on-first-init window once the pending admin
// record is consumed. The pins are the REAL barrierinit.Pins over an
// in-process KV, so the thumbprint compared is the one PinFirstAdmin wrote.
// ---------------------------------------------------------------------------

// pinKV is an in-process barrierinit.KV.
type pinKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *pinKV) Get(_ context.Context, key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok, nil
}

func (k *pinKV) PutIf(_ context.Context, key string, expected, value []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !bytes.Equal(k.m[key], expected) {
		return false, nil
	}
	if k.m == nil {
		k.m = make(map[string][]byte)
	}
	k.m[key] = value
	return true, nil
}

func (k *pinKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string][]byte)
	for key, v := range k.m {
		if strings.HasPrefix(key, prefix) {
			out[key] = v
		}
	}
	return out, nil
}

// tenantPins is FirstAdminPins over barrierinit.Pins, as
// lifecycle.BarrierInit.TenantFirstAdmin reads them.
type tenantPins struct {
	pins *barrierinit.Pins
	err  error
}

func (p tenantPins) TenantFirstAdmin(ctx context.Context, tenantID string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return p.pins.FirstAdmin(ctx, clusterseal.TenantBarrier(tenantID))
}

// pinnedFirstAdmin pins thumbprint for tenantID's barrier.
func pinnedFirstAdmin(t *testing.T, tenantID, thumbprint string) tenantPins {
	t.Helper()
	pins := barrierinit.NewPins(&pinKV{})
	if err := pins.PinFirstAdmin(context.Background(), clusterseal.TenantBarrier(tenantID), thumbprint); err != nil {
		t.Fatalf("PinFirstAdmin: %v", err)
	}
	return tenantPins{pins: pins}
}

// TestCheckFirstInitCertificate_ConsumedRecord_PinnedAdmitted proves a node
// initializing after the first admin's account was created -- the pending
// record consumed -- still admits the onboarded admin's certificate, under
// the posture that otherwise admits nobody without a record.
func TestCheckFirstInitCertificate_ConsumedRecord_PinnedAdmitted(t *testing.T) {
	ck := mintCert(t, "alice", "acme", nil, "")
	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(newPendingStore(t)),
		WithFirstAdminPins(pinnedFirstAdmin(t, "acme", user.ComputeSPKIThumbprint(ck.cert))),
		WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	if err := m.checkFirstInitCertificate(mtlsContext(ck), caller, "acme"); err != nil {
		t.Fatalf("expected the pinned admin's certificate to be admitted, got: %v", err)
	}
	thumb, err := m.initProofThumbprint(mtlsContext(mintCert(t, "bob", "acme", nil, "")), "acme")
	if err != nil || thumb != user.ComputeSPKIThumbprint(ck.cert) {
		t.Fatalf("initProofThumbprint = %q, %v; want the pinned admin's, not the presented one", thumb, err)
	}
}

// TestCheckFirstInitCertificate_ConsumedRecord_OtherRefused proves the pin
// confines the window as the record did: any other certificate of the
// tenant is refused, even under the "enrolled" posture that admits any
// certificate when neither exists.
func TestCheckFirstInitCertificate_ConsumedRecord_OtherRefused(t *testing.T) {
	onboarded := mintCert(t, "alice", "acme", nil, "")
	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(newPendingStore(t)),
		WithFirstAdminPins(pinnedFirstAdmin(t, "acme", user.ComputeSPKIThumbprint(onboarded.cert))),
		WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	for name, ctx := range map[string]context.Context{
		"other certificate": mtlsContext(mintCert(t, "alice", "acme", nil, "")),
		"no certificate":    context.Background(),
	} {
		err := m.checkFirstInitCertificate(ctx, caller, "acme")
		var mismatch *FirstInitCertificateMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("%s: expected *FirstInitCertificateMismatchError, got %T: %v", name, err, err)
		}
		if strings.Contains(mismatch.Error(), "admin  was") || !strings.Contains(mismatch.Error(), "its first admin was onboarded") {
			t.Fatalf("%s: Error() = %q", name, mismatch.Error())
		}
	}
}

// TestCheckFirstInitCertificate_PendingRecordWinsOverPin proves the pending
// record, while it exists, decides: it names the admin as well as the key.
func TestCheckFirstInitCertificate_PendingRecordWinsOverPin(t *testing.T) {
	ck := mintCert(t, "alice", "acme", nil, "")
	store := newPendingStore(t)
	if err := store.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username: "alice", Thumbprint: user.ComputeSPKIThumbprint(ck.cert),
	}); err != nil {
		t.Fatal(err)
	}
	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(store),
		WithFirstAdminPins(pinnedFirstAdmin(t, "acme", user.ComputeSPKIThumbprint(ck.cert))))
	err := m.checkFirstInitCertificate(mtlsContext(ck), &auth.Identity{Subject: "mallory", TenantID: "acme"}, "acme")
	var mismatch *FirstInitCertificateMismatchError
	if !errors.As(err, &mismatch) || mismatch.Username != "alice" {
		t.Fatalf("expected the pending record's mismatch naming alice, got %T: %v", err, err)
	}
}

// TestCheckFirstInitCertificate_NoPin_FallsThroughToPosture proves a tenant
// with neither record nor pin is decided by the posture, as before.
func TestCheckFirstInitCertificate_NoPin_FallsThroughToPosture(t *testing.T) {
	ck := mintCert(t, "alice", "acme", nil, "")
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	empty := tenantPins{pins: barrierinit.NewPins(&pinKV{})}
	required := New(&mockAuthorizer{}, WithFirstAdminPins(empty), WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	var notOnboarded *FirstInitNotOnboardedError
	if err := required.checkFirstInitCertificate(mtlsContext(ck), caller, "acme"); !errors.As(err, &notOnboarded) {
		t.Fatalf("required: expected *FirstInitNotOnboardedError, got %T: %v", err, err)
	}
	enrolled := New(&mockAuthorizer{}, WithFirstAdminPins(empty), WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	if err := enrolled.checkFirstInitCertificate(mtlsContext(ck), caller, "acme"); err != nil {
		t.Fatalf("enrolled: expected admission, got %v", err)
	}
	if thumb, err := enrolled.initProofThumbprint(mtlsContext(ck), "acme"); err != nil || thumb != user.ComputeSPKIThumbprint(ck.cert) {
		t.Fatalf("initProofThumbprint = %q, %v; want the presented certificate's", thumb, err)
	}
}

// TestCheckFirstInitCertificate_PinsUnreadable_FailsClosed proves a pin that
// cannot be read refuses the window and the proof rather than opening them.
func TestCheckFirstInitCertificate_PinsUnreadable_FailsClosed(t *testing.T) {
	ck := mintCert(t, "alice", "acme", nil, "")
	want := errors.New("system store unavailable")
	m := New(&mockAuthorizer{}, WithFirstAdminPins(tenantPins{err: want}), WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	var unavailable *StoreUnavailableError
	if err := m.checkFirstInitCertificate(mtlsContext(ck), &auth.Identity{Subject: "alice", TenantID: "acme"}, "acme"); !errors.As(err, &unavailable) || !errors.Is(err, want) {
		t.Fatalf("checkFirstInitCertificate: expected *StoreUnavailableError wrapping the read error, got %T: %v", err, err)
	}
	if _, err := m.initProofThumbprint(mtlsContext(ck), "acme"); !errors.As(err, &unavailable) {
		t.Fatalf("initProofThumbprint: expected *StoreUnavailableError, got %T: %v", err, err)
	}
}
