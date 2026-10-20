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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// testLeafCAForMultiNode is a minimal signing CA for this file's own tests:
// one CA keypair, and Issue mints a fresh leaf chaining to it on every call
// -- N distinct admin certificates, one per simulated node, all trusting the
// SAME pool a real cluster's single ceremony CA would produce.
type testLeafCAForMultiNode struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	pool   *x509.CertPool
}

func newTestLeafCA(t *testing.T) *testLeafCAForMultiNode {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (CA): %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test multi-node CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (CA): %v", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate (CA): %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return &testLeafCAForMultiNode{caCert: caCert, caKey: caKey, pool: pool}
}

// issueLeaf mints a fresh, DISTINCT admin certificate chaining to this CA --
// simulating the distinct certificate an independently-run ceremony on one
// cluster node would produce.
func (ca *testLeafCAForMultiNode) issueLeaf(t *testing.T, serial int64) []byte {
	t.Helper()
	subject, uris := presentedIdentity(t, tenantid.System, tenantid.System, presentedTrustDomain)
	return ca.issueLeafAs(t, serial, subject, uris)
}

// issueLeafAs issues a leaf naming subject and the URI SANs uris.
func (ca *testLeafCAForMultiNode) issueLeafAs(t *testing.T, serial int64, subject pkix.Name, uris []*url.URL) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (leaf): %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      subject,
		URIs:         uris,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestMultiNode_ExactlyOneCredentialWinsAndIsBound is the B4 end-to-end
// proof (go-qrdb#548 review): N nodes each issue their OWN, genuinely
// DIFFERENT admin certificate -- exactly what N independently-run `qrdb
// init` calls against the same cluster CA would produce -- and race to
// record it via WritePendingAdminCert's create-if-absent guard. Exactly one
// wins (TestWritePendingAdminCert_ConcurrentNodesRaceDeterministically
// already pins that at the storage layer). This test carries the race one
// layer further: ALL N nodes then read back whatever record won and call
// Initialize with it CONCURRENTLY, as if each still believed it might be
// the one to run first boot. Exactly one admin account is created, bound to
// EXACTLY the winning credential's SPKI thumbprint; none of the other N-1
// credentials are ever bound to anything, and no second admin account is
// created.
func TestMultiNode_ExactlyOneCredentialWinsAndIsBound(t *testing.T) {
	const nodes = 8

	kv := newGuardedMemoryKVStore()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(kv, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	tenantSvc, err := tenant.NewKVTenantService(kv, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	ca := newTestLeafCA(t)
	certPEMs := make([][]byte, nodes)
	for i := range certPEMs {
		certPEMs[i] = ca.issueLeaf(t, int64(i+2))
	}

	// Phase 1: N nodes race to record their own credential. Exactly one
	// wins; this mirrors TestWritePendingAdminCert_ConcurrentNodesRaceDeterministically
	// over the SAME mechanism, here with real, CA-chained certificates
	// rather than placeholder bytes.
	store := newMemPendingAdminCertStore()
	var wg sync.WaitGroup
	writeErrs := make([]error, nodes)
	for i := 0; i < nodes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writeErrs[i] = WritePendingAdminCert(context.Background(), store, certPEMs[i])
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, err := range writeErrs {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("pending-cert write wins = %d, want exactly 1", wins)
	}

	winningCertPEM, err := store.Get(context.Background(), PendingAdminCertKey)
	if err != nil {
		t.Fatalf("Get winning record: %v", err)
	}

	// Phase 2: ALL N nodes read the SAME winning record (not necessarily
	// their own) and race to run Initialize with it concurrently -- exactly
	// what N nodes that each independently resolved AdminCertPEM from the
	// SAME replicated pending-cert record via ResolveAdminCertForFirstBoot
	// would do. In a real deployment "only the leader may create" bounds
	// this to one writer at a time; raw N-way concurrency on the SAME
	// record is a strictly HARDER case than that.
	//
	// Each node runs Initialize ONCE and must succeed: a node that loses the
	// admin create converges on the winner's admin inside Initialize
	// (go-qrdb#668). A retry loop here would hide exactly that failure -- a
	// loser refusing the winner's in-flight create -- behind a later attempt
	// that finds the record written.
	binder := UserCertBinder{Users: userSvc}
	runNode := func() error {
		_, err := Initialize(context.Background(), Dependencies{
			Tenants: tenantSvc, Users: userSvc, Roles: &fakeRoles{}, Logger: discardLogger(),
			AdminCertPEM: winningCertPEM, AdminCertPool: ca.pool,
			Binder: binder,
		})
		return err
	}
	initErrs := make([]error, nodes)
	for i := 0; i < nodes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			initErrs[i] = runNode()
		}(i)
	}
	wg.Wait()

	for i, err := range initErrs {
		if err != nil {
			t.Fatalf("node %d Initialize: %v", i, err)
		}
	}

	// Exactly one admin account exists, bound to exactly the winning
	// credential's thumbprint -- never a mix of several nodes' bindings,
	// never a second account.
	got, gerr := userSvc.GetUserByUsername(context.Background(), tenantid.System, adminUsername)
	if gerr != nil {
		t.Fatalf("GetUserByUsername: %v", gerr)
	}
	winningThumbprint, tperr := spkiThumbprintFromCertPEM(winningCertPEM)
	if tperr != nil {
		t.Fatalf("spkiThumbprintFromCertPEM: %v", tperr)
	}
	if len(got.CertBindings) != 1 {
		t.Fatalf("admin CertBindings = %+v, want exactly 1 (the winning credential, bound once despite %d concurrent Initialize calls)",
			got.CertBindings, nodes)
	}
	if got.CertBindings[0].Thumbprint != winningThumbprint {
		t.Fatalf("bound thumbprint = %q, want the winning credential's %q", got.CertBindings[0].Thumbprint, winningThumbprint)
	}

	// None of the OTHER N-1 credentials -- the ones that lost the pending-cert
	// race and were never read back by any node -- were ever bound.
	for i, certPEM := range certPEMs {
		if string(certPEM) == string(winningCertPEM) {
			continue
		}
		losingThumbprint, tperr := spkiThumbprintFromCertPEM(certPEM)
		if tperr != nil {
			t.Fatalf("spkiThumbprintFromCertPEM (node %d): %v", i, tperr)
		}
		for _, b := range got.CertBindings {
			if b.Thumbprint == losingThumbprint {
				t.Fatalf("node %d's LOSING credential was bound; only the winning credential may ever be bound", i)
			}
		}
	}

	// Exactly one admin record total -- no second account under a
	// colliding id or a mangled username.
	list, lerr := userSvc.ListUsers(context.Background(), &user.ListUsersRequest{TenantID: tenantid.System})
	if lerr != nil {
		t.Fatalf("ListUsers: %v", lerr)
	}
	admins := 0
	for _, u := range list.Users {
		if u.Username == adminUsername {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("admin accounts in the system tenant = %d, want exactly 1 despite %d concurrent Initialize calls", admins, nodes)
	}
}
