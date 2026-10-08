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

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/dek/dektest"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// askedLimiter is a ratelimit.Limiter that records each key it is asked about.
type askedLimiter struct {
	mu    sync.Mutex
	allow bool
	keys  []string
}

func (l *askedLimiter) Allow(key string, _ ratelimit.Policy) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	return l.allow, time.Second
}

func (l *askedLimiter) Close() error { return nil }

func (l *askedLimiter) asked() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.keys...)
}

var _ ratelimit.Limiter = (*askedLimiter)(nil)

func buildSeamNode(t *testing.T, infra lifecycle.Infra) *lifecycle.Node {
	t.Helper()
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	infra.Host = host
	infra.WatchHub = hub
	infra.DataDir = t.TempDir()
	node, err := lifecycle.BuildServices(context.Background(), infra)
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	return node
}

// buildSealedSeamNode is buildSeamNode with the seal subsystem on, which is
// where the enrolment services (and so the invitation service) are built.
func buildSealedSeamNode(t *testing.T, infra lifecycle.Infra) *lifecycle.Node {
	t.Helper()
	barrier, _, credSvc := newTestBarrier(t)
	infra.SealBarrier = barrier
	infra.BarrierRegistry = newPooledRegistry(t, barrier)
	infra.CredentialCoreSvc = credSvc
	infra.KeyWrapRegistry = fakeSoftwareKeyWrapRegistry()
	return buildSeamNode(t, infra)
}

// TestBuildServices_DEKProviderReplacesTheKeyring: an injected provider serves
// the tenant DEK service, and no key bytes or key-wrap provider are needed.
func TestBuildServices_DEKProviderReplacesTheKeyring(t *testing.T) {
	device := dektest.New()
	node := buildSeamNode(t, lifecycle.Infra{DEKProvider: device})
	if node.DEK == nil {
		t.Fatal("DEK is nil although a provider was injected")
	}
	ctx := context.Background()
	if _, err := node.Tenant.CreateTenant(ctx, "acme", "acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	acme := tenant.WithTenantID(ctx, "acme")
	sealed, err := node.DEK.Encrypt(acme, "", []byte("s3cret"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	opened, err := node.DEK.Decrypt(acme, "", sealed.Ciphertext, []byte("aad"))
	if err != nil || !bytes.Equal(opened.Plaintext, []byte("s3cret")) {
		t.Fatalf("Decrypt = %v, %v", opened, err)
	}
	if len(device.Ops()) == 0 {
		t.Fatal("the requests never reached the device")
	}
	if _, err := node.DEK.Status(tenant.WithTenantID(ctx, "globex"), "acme"); !errors.Is(err, dek.ErrCrossTenant) {
		t.Fatalf("cross-tenant over a device: %v, want ErrCrossTenant", err)
	}
}

// TestBuildServices_InvitationRedemptionIsThrottledByTheComposersLimiter: an
// injected limiter replaces the node's own bucket and is asked once per source
// address first.
func TestBuildServices_InvitationRedemptionIsThrottledByTheComposersLimiter(t *testing.T) {
	deny := &askedLimiter{}
	node := buildSealedSeamNode(t, lifecycle.Infra{InvitationRedeemLimiter: deny})
	if node.Invitations == nil {
		t.Fatal("invitation service not built")
	}
	_, err := node.Invitations.RedeemInvitation(context.Background(), &service.RedeemInvitationRequest{
		TenantID: "acme", Token: "t", CSRPEM: []byte("csr"),
	})
	var invalid *service.InvitationInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("throttled redemption: err = %v, want the opaque *service.InvitationInvalidError", err)
	}
	keys := deny.asked()
	if len(keys) == 0 {
		t.Fatal("the composer's limiter was never consulted")
	}
	if want := "invitation.redeem\x00source\x00"; len(keys[0]) < len(want) || keys[0][:len(want)] != want {
		t.Fatalf("first key = %q, want the per-source key", keys[0])
	}
}

func TestBuildServices_InvitationRedemptionHasABuiltInLimiterByDefault(t *testing.T) {
	node := buildSealedSeamNode(t, lifecycle.Infra{})
	if node.Invitations == nil {
		t.Fatal("invitation service not built")
	}
	// The default per-node bucket admits the burst, then refuses with the same
	// opaque error a bad token gets; either way the caller learns nothing.
	for i := 0; i < 200; i++ {
		_, err := node.Invitations.RedeemInvitation(context.Background(), &service.RedeemInvitationRequest{
			TenantID: "acme", Token: "t", CSRPEM: []byte("csr"),
		})
		var invalid *service.InvitationInvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("redemption %d: err = %v, want *service.InvitationInvalidError", i, err)
		}
	}
}

func TestQuorumLimitWith_UsesTheInjectedLimiter(t *testing.T) {
	deny := &askedLimiter{}
	limit := lifecycle.QuorumLimitWith(deny)
	ok, retry := limit("holder-1", false)
	if ok || retry != time.Second {
		t.Fatalf("limit = %v, %v; want the injected limiter's refusal", ok, retry)
	}
	if got := deny.asked(); len(got) != 1 || got[0] != "holder-1" {
		t.Fatalf("asked = %v, want [holder-1]", got)
	}
}

func TestQuorumLimitWith_NilIsTheBuiltInLimit(t *testing.T) {
	limit := lifecycle.QuorumLimitWith(nil)
	if ok, _ := limit("quorum-limit-with-nil-default", false); !ok {
		t.Fatal("the built-in limit refused the first request")
	}
}
