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

package clienttrust

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
)

// settlePool is a pool over prov wired onto the returned listener config.
// built runs the first rebuild, so the snapshot trusts prov's CAs as they
// are now; otherwise no rebuild has run.
func settlePool(t *testing.T, prov *fakeCertProvider, built bool) (*Pool, *tls.Config) {
	t.Helper()
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(prov)
	if built {
		rebuild(t, pool)
	}
	return pool, cfg
}

// TestPool_Settle_AdmitsACAAnnouncedByInvalidate is the issuance race: a
// tenant's login CA comes into use with its first certificate and is
// announced by Invalidate, whose rebuild runs on Run's goroutine. Until it
// lands the handshake refuses the certificate; Settle is what a caller
// answering with that certificate runs so the next handshake admits it.
func TestPool_Settle_AdmitsACAAnnouncedByInvalidate(t *testing.T) {
	firstPEM, _ := genCAPEM(t)
	prov := &fakeCertProvider{pems: [][]byte{firstPEM}}
	pool, cfg := settlePool(t, prov, true)

	newPEM, newCert := genCAPEM(t)
	prov.pems = append(prov.pems, newPEM)
	pool.Invalidate()
	assertNotTrustedByConnection(t, getConfigForClient(t, cfg), newCert)

	if err := pool.Settle(context.Background()); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	assertTrustedByConnection(t, getConfigForClient(t, cfg), newCert)
}

// TestPool_Settle_NothingPendingReadsNoStorage proves Settle costs nothing
// when every announced change is already in the snapshot: an issuance that
// brought no CA into use must not pay for a rebuild.
func TestPool_Settle_NothingPendingReadsNoStorage(t *testing.T) {
	keyBPEM, _ := genCAPEM(t)
	calls := 0
	pool, _ := settlePool(t, &fakeCertProvider{pems: [][]byte{keyBPEM}, callCounts: &calls}, true)
	before := calls

	for i := 0; i < 3; i++ {
		if err := pool.Settle(context.Background()); err != nil {
			t.Fatalf("Settle: %v", err)
		}
	}
	if calls != before {
		t.Errorf("Settle with nothing pending read Key B %d times, want 0", calls-before)
	}
}

// TestPool_Settle_RebuildsOncePerAnnouncement proves a settled change is not
// rebuilt again: the first Settle after Invalidate reads, the next does not.
func TestPool_Settle_RebuildsOncePerAnnouncement(t *testing.T) {
	keyBPEM, _ := genCAPEM(t)
	calls := 0
	pool, _ := settlePool(t, &fakeCertProvider{pems: [][]byte{keyBPEM}, callCounts: &calls}, true)

	pool.Invalidate()
	before := calls
	if err := pool.Settle(context.Background()); err != nil {
		t.Fatalf("first Settle: %v", err)
	}
	if err := pool.Settle(context.Background()); err != nil {
		t.Fatalf("second Settle: %v", err)
	}
	if got := calls - before; got != 1 {
		t.Errorf("two Settles after one Invalidate read Key B %d times, want 1", got)
	}
}

// TestPool_Settle_NeverBuiltBuilds proves Settle on a pool no rebuild has
// run for yet builds it, rather than trusting an empty mark.
func TestPool_Settle_NeverBuiltBuilds(t *testing.T) {
	keyBPEM, keyBCert := genCAPEM(t)
	pool, cfg := settlePool(t, &fakeCertProvider{pems: [][]byte{keyBPEM}}, false)

	if err := pool.Settle(context.Background()); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	assertTrustedByConnection(t, getConfigForClient(t, cfg), keyBCert)
}

// TestPool_Settle_FailedRebuildStaysPending proves a rebuild that could not
// read Key B does not settle the change it was for: the next Settle reads
// again and admits the CA once the read succeeds.
func TestPool_Settle_FailedRebuildStaysPending(t *testing.T) {
	keyBPEM, _ := genCAPEM(t)
	calls := 0
	prov := &fakeCertProvider{pems: [][]byte{keyBPEM}, callCounts: &calls}
	pool, cfg := settlePool(t, prov, true)

	newPEM, newCert := genCAPEM(t)
	prov.pems = append(prov.pems, newPEM)
	prov.err = errors.New("store unavailable")
	pool.Invalidate()
	if err := pool.Settle(context.Background()); err == nil {
		t.Fatal("Settle over a failing Key B read returned nil")
	}

	prov.err = nil
	before := calls
	if err := pool.Settle(context.Background()); err != nil {
		t.Fatalf("Settle after the read recovered: %v", err)
	}
	if calls == before {
		t.Fatal("Settle after a failed rebuild did not read Key B again")
	}
	assertTrustedByConnection(t, getConfigForClient(t, cfg), newCert)
}
