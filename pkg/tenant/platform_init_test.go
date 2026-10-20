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

package tenant

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// platformSignerDouble answers every sign with raw and err, recording the
// tenants it signed for.
type platformSignerDouble struct {
	mu    sync.Mutex
	raw   []byte
	err   error
	asked []string
}

func (p *platformSignerDouble) SignPlatformInit(tenantID string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, tenantID)
	return p.raw, p.err
}

var _ PlatformInitSigner = (*platformSignerDouble)(nil)

// isolatedTypes is every isolation type whose home group placement spreads
// across the cluster's nodes (barrierRoutedIsolation).
var isolatedTypes = []IsolationType{IsolationBarrier, IsolationVolume, IsolationRaft}

// newPlatformService is a service entitled to every isolated type and to
// platform custody, whose inits claim through book, with signer as its
// platform signer (none when nil). Its system barrier is unsealed: it seals
// every platform-custody key.
func newPlatformService(t *testing.T, book *bookDouble, signer PlatformInitSigner) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	ents := raftEntitlements()
	ents.AllowedIsolationModes = append(ents.AllowedIsolationModes, quota.IsolationModeBarrier, quota.IsolationModeVolume)
	ents.AllowVolumeStorage = true
	ents.AllowSystemManagedBarrier = true
	// newTestBarrierRegistry has already initialized the system barrier a
	// platform-custody tenant's key is sealed to.
	svc, reg := newRaftService(t, WithBarrierInitBook(book), WithDefaultEntitlements(ents))
	if signer != nil {
		svc.SetPlatformInitSigner(signer)
	}
	return svc, reg
}

// An isolated tenant created in platform custody claims the platform's
// statement, for the system barrier, once its record has committed: its home
// group is placed on every node, and each of them initializes its own key
// from that statement.
func TestCreateTenant_ClaimsThePlatformStatement(t *testing.T) {
	for _, isolation := range isolatedTypes {
		t.Run(string(isolation), func(t *testing.T) {
			book := &bookDouble{}
			signer := &platformSignerDouble{raw: []byte("platform-signed")}
			svc, _ := newPlatformService(t, book, signer)
			if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", isolation, WithBarrierAutoUnseal(true)); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}
			want := initClaim{tenantID: "acme", strategy: seal.StrategySystemBarrier, statement: "platform-signed"}
			if got := book.claimed(); len(got) != 1 || got[0] != want {
				t.Fatalf("claims = %+v, want [%+v]", got, want)
			}
			if len(signer.asked) != 1 || signer.asked[0] != "acme" {
				t.Fatalf("signer asked for %v, want [acme]", signer.asked)
			}
		})
	}
}

// Only an isolated tenant in platform custody claims a platform statement:
// an owner keeps custody of the rest, and a prefix tenant has no barrier.
func TestCreateTenant_NoPlatformClaim(t *testing.T) {
	for name, tc := range map[string]struct {
		isolation  IsolationType
		autoUnseal bool
		noSigner   bool
	}{
		"owner custody": {isolation: IsolationRaft},
		"not isolated":  {isolation: IsolationPrefix, autoUnseal: true},
		// A standalone node composes no platform signer and serves each
		// barrier on itself alone: a tenant it can host has no other node to
		// initialize from a statement.
		"no platform signer/barrier": {isolation: IsolationBarrier, autoUnseal: true, noSigner: true},
		"no platform signer/volume":  {isolation: IsolationVolume, autoUnseal: true, noSigner: true},
	} {
		t.Run(name, func(t *testing.T) {
			book := &bookDouble{}
			signer := &platformSignerDouble{raw: []byte("signed")}
			var wired PlatformInitSigner = signer
			if tc.noSigner {
				wired = nil
			}
			svc, _ := newPlatformService(t, book, wired)
			if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tc.isolation, WithBarrierAutoUnseal(tc.autoUnseal)); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}
			if got := book.claimed(); len(got) != 0 {
				t.Fatalf("claims = %+v, want none", got)
			}
			if len(signer.asked) != 0 {
				t.Fatalf("signer asked for %v, want nothing", signer.asked)
			}
		})
	}
}

// A platform statement that cannot be signed or claimed undoes the create: no
// record and no barrier remain, so the tenant can be created again. A node
// with no platform signer has nothing any other node could verify, so it
// refuses the create rather than serve the barrier on one node only.
func TestCreateTenant_ARefusedPlatformClaimUndoesTheCreate(t *testing.T) {
	refused := errors.New("claim refused")
	unsigned := errors.New("signer unavailable")
	for name, tc := range map[string]struct {
		book       *bookDouble
		signer     PlatformInitSigner
		check      func(error) bool
		isolations []IsolationType // isolatedTypes when nil
	}{
		"refused claim": {
			book: &bookDouble{err: refused}, signer: &platformSignerDouble{raw: []byte("signed")},
			check: func(err error) bool { return errors.Is(err, refused) },
		},
		"signing fails": {
			book: &bookDouble{}, signer: &platformSignerDouble{err: unsigned},
			check: func(err error) bool {
				var berr *BarrierError
				return errors.Is(err, unsigned) && errors.As(err, &berr) && berr.Op == "init statement"
			},
		},
		// Only a raft tenant is refused without a signer: its groups exist
		// only across a cluster's nodes, while a standalone node, which
		// composes none, hosts a barrier or volume tenant on itself alone.
		"no platform signer": {
			book: &bookDouble{},
			check: func(err error) bool {
				var req *InitStatementRequiredError
				return errors.As(err, &req) && req.TenantID == "acme"
			},
			isolations: []IsolationType{IsolationRaft},
		},
	} {
		isolations := tc.isolations
		if isolations == nil {
			isolations = isolatedTypes
		}
		for _, isolation := range isolations {
			t.Run(name+"/"+string(isolation), func(t *testing.T) {
				svc, reg := newPlatformService(t, tc.book, tc.signer)
				ctx := context.Background()
				_, err := svc.CreateTenant(ctx, "acme", "Acme", isolation, WithBarrierAutoUnseal(true))
				if !tc.check(err) {
					t.Fatalf("CreateTenant = %v", err)
				}
				if _, err := svc.GetTenant(ctx, "acme"); !errors.Is(err, ErrTenantNotFound) {
					t.Fatalf("GetTenant after an undone create = %v, want ErrTenantNotFound", err)
				}
				if _, err := reg.GetTenant("acme"); !errors.Is(err, seal.ErrTenantNotFound) {
					t.Fatalf("registry GetTenant after an undone create = %v, want seal.ErrTenantNotFound", err)
				}
			})
		}
	}
}

// A node joining a platform-custody barrier initializes its own key under its
// own system barrier from the platform's statement; a redelivery is done.
func TestTenantBarrierSelfInit_PlatformCustody(t *testing.T) {
	svc, reg := newPlatformService(t, &bookDouble{}, &platformSignerDouble{raw: []byte("signed")})
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationRaft, WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// This node joins: it holds the record and the tenant's volume assignment,
	// and neither the barrier nor its data.
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "acme")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot: %v", err)
	}
	if err := reg.ReleaseTenant(ctx, "acme"); err != nil {
		t.Fatalf("ReleaseTenant: %v", err)
	}
	if err := os.RemoveAll(dataRoot); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	for range 2 {
		if err := svc.TenantBarrierSelfInit(ctx, "acme", seal.StrategySystemBarrier, ""); err != nil {
			t.Fatalf("TenantBarrierSelfInit: %v", err)
		}
		tb, err := reg.GetTenant("acme")
		if err != nil {
			t.Fatalf("registry GetTenant: %v", err)
		}
		if !initialized(t, svc, "acme") {
			t.Fatal("the joined platform-custody barrier is not initialized")
		}
		if tb.IsSealed() {
			t.Fatal("the joined platform-custody barrier is sealed")
		}
	}
}

// A system-barrier statement names no barrier whose owner keeps custody.
func TestTenantBarrierSelfInit_PlatformStatementForAnOwnersBarrier(t *testing.T) {
	svc, reg := newPlatformService(t, &bookDouble{}, nil)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationRaft); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	var berr *BarrierError
	err := svc.TenantBarrierSelfInit(ctx, "acme", seal.StrategySystemBarrier, "")
	if !errors.Is(err, ErrNotPlatformCustody) || !errors.As(err, &berr) || berr.Op != "initialize" {
		t.Fatalf("SelfInit = %v, want an initialize *BarrierError wrapping ErrNotPlatformCustody", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("registry GetTenant: %v", err)
	}
	if !tb.IsSealed() {
		t.Fatal("an owner's barrier was unsealed by a platform statement")
	}
}

// A sealed platform-custody barrier unseals with the platform strategy its
// key is sealed under, as self-init initialized it: the default composition
// has no key provider that resolves the system barrier's strategy, and none
// is needed. This holds on the node that created the tenant and on one that
// joined its home group.
func TestTenantBarrierUnseal_PlatformCustody(t *testing.T) {
	for _, isolation := range isolatedTypes {
		for _, joined := range []bool{false, true} {
			name := string(isolation) + "/creating node"
			if joined {
				name = string(isolation) + "/joined node"
			}
			t.Run(name, func(t *testing.T) {
				svc, reg := newPlatformService(t, &bookDouble{}, &platformSignerDouble{raw: []byte("signed")})
				ctx := WithTenantID(context.Background(), "acme")
				if _, err := svc.CreateTenant(ctx, "acme", "Acme", isolation, WithBarrierAutoUnseal(true)); err != nil {
					t.Fatalf("CreateTenant: %v", err)
				}
				if joined {
					joinWithoutBarrier(t, reg, "acme")
					if err := svc.TenantBarrierSelfInit(ctx, "acme", seal.StrategySystemBarrier, ""); err != nil {
						t.Fatalf("TenantBarrierSelfInit: %v", err)
					}
				}
				if err := reg.SealTenant("acme"); err != nil {
					t.Fatalf("SealTenant: %v", err)
				}

				if err := svc.TenantBarrierUnseal(ctx, "acme", []byte("unused")); err != nil {
					t.Fatalf("TenantBarrierUnseal: %v", err)
				}
				tb, err := reg.GetTenant("acme")
				if err != nil {
					t.Fatalf("registry GetTenant: %v", err)
				}
				if tb.IsSealed() {
					t.Fatal("the platform-custody barrier is still sealed")
				}
			})
		}
	}
}

// A system-barrier statement unseals no barrier whose owner keeps custody.
func TestTenantBarrierUnseal_PlatformStatementForAnOwnersBarrier(t *testing.T) {
	book := &bookDouble{recorded: map[string]BarrierInitTerms{"acme": {StrategyID: seal.StrategySystemBarrier, Epoch: 1}}}
	svc, reg := newPlatformService(t, book, nil)
	ctx := WithTenantID(context.Background(), "acme")
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationRaft); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	var berr *BarrierError
	err := svc.TenantBarrierUnseal(ctx, "acme", []byte("unused"))
	if !errors.Is(err, ErrNotPlatformCustody) || !errors.As(err, &berr) || berr.Op != "unseal" {
		t.Fatalf("TenantBarrierUnseal = %v, want an unseal *BarrierError wrapping ErrNotPlatformCustody", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("registry GetTenant: %v", err)
	}
	if !tb.IsSealed() {
		t.Fatal("an owner's barrier was unsealed by a platform statement")
	}
}
