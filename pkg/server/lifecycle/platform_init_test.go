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

package lifecycle

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// custodyDouble answers each tenant's record from infos, or fails with err,
// counting the reads.
type custodyDouble struct {
	infos map[string]*tenant.TenantInfo
	err   error
	reads int
}

func (d *custodyDouble) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	d.reads++
	if d.err != nil {
		return nil, d.err
	}
	if info, ok := d.infos[tenantID]; ok {
		return info, nil
	}
	return nil, tenant.ErrTenantNotFound
}

var _ tenantCustody = (*custodyDouble)(nil)

// platformWiringDouble is a tenant service recording the platform signer it
// is handed.
type platformWiringDouble struct {
	tenant.TenantServicer
	signer tenant.PlatformInitSigner
}

func (d *platformWiringDouble) SetPlatformInitSigner(signer tenant.PlatformInitSigner) {
	d.signer = signer
}

var _ tenant.PlatformInitWiring = (*platformWiringDouble)(nil)

// unwiredTenantService is a tenant service that takes no platform signer.
type unwiredTenantService struct {
	tenant.TenantServicer
}

// platformIdentity is a cluster CA's pool and a node identity it issued.
// platformNow is the instant the platform identity's certificates are valid
// around (an hour either side), and the time on a test BarrierInit's clock.
var platformNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func platformIdentity(t *testing.T) (*x509.CertPool, *tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cluster-ca"},
		NotBefore: platformNow.Add(-time.Hour), NotAfter: platformNow.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nodeTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "node-1"},
		NotBefore: platformNow.Add(-time.Hour), NotAfter: platformNow.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	nodeDER, err := x509.CreateCertificate(rand.Reader, nodeTmpl, ca, &nodeKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, &tls.Certificate{Certificate: [][]byte{nodeDER}, PrivateKey: nodeKey}
}

// newTestBarrierInit is a BarrierInit over an in-memory system store.
func newTestBarrierInit(t *testing.T) *BarrierInit {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	return newBarrierInit(service.NewKVService(backend.NewLocalBackend(engine.NewKVEngine(stg)), 0), clocktest.NewAt(platformNow))
}

// TestBarrierInit_TenantFirstAdmin proves the auth middleware reads the
// thumbprint pinned for the tenant's own barrier, and no other's.
func TestBarrierInit_TenantFirstAdmin(t *testing.T) {
	bi := newTestBarrierInit(t)
	ctx := context.Background()
	if err := bi.Anchors.PinFirstAdmin(ctx, clusterseal.TenantBarrier("acme"), "thumb-acme"); err != nil {
		t.Fatal(err)
	}
	if got, err := bi.TenantFirstAdmin(ctx, "acme"); err != nil || got != "thumb-acme" {
		t.Fatalf("TenantFirstAdmin(acme) = %q, %v; want thumb-acme", got, err)
	}
	if got, err := bi.TenantFirstAdmin(ctx, "globex"); err != nil || got != "" {
		t.Fatalf("TenantFirstAdmin(globex) = %q, %v; want none pinned", got, err)
	}
}

// TestBarrierInit_TenantFirstAdmin_CorruptPin proves an unreadable pin is an
// error, which the middleware turns into a refusal, not an empty answer.
func TestBarrierInit_TenantFirstAdmin_CorruptPin(t *testing.T) {
	bi := newTestBarrierInit(t)
	ctx := context.Background()
	if err := bi.Anchors.PinFirstAdmin(ctx, clusterseal.TenantBarrier("acme"), "thumb-acme"); err != nil {
		t.Fatal(err)
	}
	key := barrierinit.AnchorPrefix + string(clusterseal.TenantBarrier("acme"))
	raw, found, err := bi.KV.Get(ctx, key)
	if err != nil || !found {
		t.Fatalf("Get(%s) = %t, %v; want the pin", key, found, err)
	}
	if ok, err := bi.KV.PutIf(ctx, key, raw, []byte("{")); err != nil || !ok {
		t.Fatalf("corrupt the pin: %t, %v", ok, err)
	}
	var corrupt *barrierinit.CorruptRecordError
	if _, err := bi.TenantFirstAdmin(ctx, "acme"); !errors.As(err, &corrupt) {
		t.Fatalf("TenantFirstAdmin = %v; want *barrierinit.CorruptRecordError", err)
	}
}

// The node's composition end to end: the platform's statement, signed by the
// signer wirePlatformInit gives the tenant service, is claimed for a tenant
// that chose platform custody and refused for one whose owner keeps it.
func TestBarrierInit_APlatformStatementVerifiesOnlyForAPlatformCustodyTenant(t *testing.T) {
	ctx := context.Background()
	bi := newTestBarrierInit(t)
	bi.tenants = &custodyDouble{infos: map[string]*tenant.TenantInfo{
		"acme":   {ID: "acme", BarrierAutoUnseal: true},
		"globex": {ID: "globex"},
	}}
	wiring := &platformWiringDouble{}
	pool, id := platformIdentity(t)
	wirePlatformInit(&Node{BarrierInit: bi, Tenant: wiring}, pool, id)
	if wiring.signer == nil {
		t.Fatal("no platform signer was wired")
	}

	raw, err := wiring.signer.SignPlatformInit("acme")
	if err != nil {
		t.Fatalf("SignPlatformInit(acme): %v", err)
	}
	if err := bi.claimer(nil).ClaimTenantInit(ctx, "acme", seal.StrategySystemBarrier, "", raw); err != nil {
		t.Fatalf("ClaimTenantInit(acme): %v", err)
	}

	raw, err = wiring.signer.SignPlatformInit("globex")
	if err != nil {
		t.Fatalf("SignPlatformInit(globex): %v", err)
	}
	var refused *barrierinit.InitStatementError
	if err := bi.claimer(nil).ClaimTenantInit(ctx, "globex", seal.StrategySystemBarrier, "", raw); !errors.As(err, &refused) || refused.Reason != barrierinit.ReasonNoAnchor {
		t.Fatalf("ClaimTenantInit(globex) = %v, want a %q refusal", err, barrierinit.ReasonNoAnchor)
	}
}

// A platform statement is verified at its issue time, so it still reads back
// verified once the creating node's certificate has expired: a replica that
// initializes later, and every unseal, still find it. The node's clock drives
// both sides, so a statement this node signs after its certificate expired is
// refused.
func TestBarrierInit_APlatformStatementOutlivesTheNodeCertificate(t *testing.T) {
	ctx := context.Background()
	bi := newTestBarrierInit(t)
	clk := clocktest.NewAt(platformNow)
	bi.clock = clk
	bi.tenants = &custodyDouble{infos: map[string]*tenant.TenantInfo{
		"acme":   {ID: "acme", BarrierAutoUnseal: true},
		"globex": {ID: "globex", BarrierAutoUnseal: true},
	}}
	wiring := &platformWiringDouble{}
	pool, id := platformIdentity(t)
	wirePlatformInit(&Node{BarrierInit: bi, Tenant: wiring}, pool, id)

	raw, err := wiring.signer.SignPlatformInit("acme")
	if err != nil {
		t.Fatalf("SignPlatformInit(acme): %v", err)
	}
	if err := bi.claimer(nil).ClaimTenantInit(ctx, "acme", seal.StrategySystemBarrier, "", raw); err != nil {
		t.Fatalf("ClaimTenantInit(acme): %v", err)
	}
	clk.Advance(48 * time.Hour)
	if _, err := bi.claimer(nil).TenantInit(ctx, "acme"); err != nil {
		t.Fatalf("TenantInit(acme) after the node certificate expired = %v, want nil", err)
	}

	raw, err = wiring.signer.SignPlatformInit("globex")
	if err != nil {
		t.Fatalf("SignPlatformInit(globex): %v", err)
	}
	var refused *barrierinit.InitStatementError
	if err := bi.claimer(nil).ClaimTenantInit(ctx, "globex", seal.StrategySystemBarrier, "", raw); !errors.As(err, &refused) || refused.Reason != barrierinit.ReasonNotValidAtIssue {
		t.Fatalf("ClaimTenantInit(globex) signed after expiry = %v, want a %q refusal", err, barrierinit.ReasonNotValidAtIssue)
	}
}

// The platform anchor is added only for a tenant barrier whose record chose
// platform custody, and only once one is set; a record that cannot be read
// fails the lookup for the watcher to retry.
func TestBarrierInit_AnchorsAddThePlatformOnlyForPlatformCustody(t *testing.T) {
	ctx := context.Background()
	unreadable := errors.New("record unreadable")
	pool := x509.NewCertPool()
	for name, tc := range map[string]struct {
		barrier   clusterseal.Barrier
		custody   *custodyDouble
		noAnchor  bool
		wantPool  bool
		wantErr   error
		wantReads int
	}{
		"platform custody":   {barrier: clusterseal.TenantBarrier("acme"), custody: &custodyDouble{infos: map[string]*tenant.TenantInfo{"acme": {BarrierAutoUnseal: true}}}, wantPool: true, wantReads: 1},
		"owner custody":      {barrier: clusterseal.TenantBarrier("acme"), custody: &custodyDouble{infos: map[string]*tenant.TenantInfo{"acme": {}}}, wantReads: 1},
		"no platform anchor": {barrier: clusterseal.TenantBarrier("acme"), custody: &custodyDouble{}, noAnchor: true},
		"a node barrier":     {barrier: clusterseal.NodeBarrier(), custody: &custodyDouble{}},
		"unreadable record":  {barrier: clusterseal.TenantBarrier("acme"), custody: &custodyDouble{err: unreadable}, wantErr: unreadable, wantReads: 1},
	} {
		t.Run(name, func(t *testing.T) {
			bi := newTestBarrierInit(t)
			bi.tenants = tc.custody
			if !tc.noAnchor {
				bi.SetPlatformAnchor(pool)
			}
			a, err := bi.anchors(ctx, tc.barrier)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("anchors = %v, want %v", err, tc.wantErr)
			}
			if got := a.Platform == pool; got != tc.wantPool {
				t.Fatalf("platform anchor present = %v, want %v", got, tc.wantPool)
			}
			if tc.custody.reads != tc.wantReads {
				t.Fatalf("records read %d times, want %d", tc.custody.reads, tc.wantReads)
			}
		})
	}
}

// Without any one piece nothing is wired: a node with no cluster identity
// neither signs platform statements nor trusts them.
func TestWirePlatformInit_WiresNothingWithoutItsParts(t *testing.T) {
	pool, id := platformIdentity(t)
	for name, tc := range map[string]struct {
		noInit bool
		pool   *x509.CertPool
		id     *tls.Certificate
	}{
		"no barrier init":        {noInit: true, pool: pool, id: id},
		"no CA pool":             {id: id},
		"no node identity":       {pool: pool},
		"no node certificate":    {pool: pool, id: &tls.Certificate{PrivateKey: id.PrivateKey}},
		"a key that cannot sign": {pool: pool, id: &tls.Certificate{Certificate: id.Certificate, PrivateKey: "not a signer"}},
	} {
		t.Run(name, func(t *testing.T) {
			wiring := &platformWiringDouble{}
			n := &Node{Tenant: wiring}
			if !tc.noInit {
				n.BarrierInit = &BarrierInit{}
			}
			wirePlatformInit(n, tc.pool, tc.id)
			if wiring.signer != nil {
				t.Fatal("a platform signer was wired")
			}
			if n.BarrierInit != nil && n.BarrierInit.platform.Load() != nil {
				t.Fatal("a platform anchor was set")
			}
		})
	}
	wirePlatformInit(nil, pool, id) // a nil node wires nothing and does not panic
}

// A tenant service that takes no platform signer still gets the anchor, so
// this node verifies the statements its peers sign.
func TestWirePlatformInit_ATenantServiceWithoutASignerStillTrustsThePlatform(t *testing.T) {
	pool, id := platformIdentity(t)
	n := &Node{BarrierInit: &BarrierInit{}, Tenant: &unwiredTenantService{}}
	wirePlatformInit(n, pool, id)
	if n.BarrierInit.platform.Load() != pool {
		t.Fatal("the platform anchor was not set")
	}
}

// ComposeRaftTenantCluster wires the platform's statement on a node with a
// Raft host and leaves it unwired without one.
func TestComposeRaftTenantCluster_WiresThePlatformInitWithAHost(t *testing.T) {
	pool, id := platformIdentity(t)
	for name, tc := range map[string]struct {
		withHost bool
		want     bool
	}{
		"with a host":    {withHost: true, want: true},
		"without a host": {},
	} {
		t.Run(name, func(t *testing.T) {
			wiring := &platformWiringDouble{}
			n := &Node{BarrierInit: &BarrierInit{}, Tenant: wiring}
			c := RaftTenantClusterConfig{Services: n, CACertPool: pool, Identity: id}
			if tc.withHost {
				c.Host = raftharness.NewCluster(t, 1).Hosts[0]
			}
			ComposeRaftTenantCluster(c)
			if got := wiring.signer != nil; got != tc.want {
				t.Fatalf("platform signer wired = %v, want %v", got, tc.want)
			}
		})
	}
}
