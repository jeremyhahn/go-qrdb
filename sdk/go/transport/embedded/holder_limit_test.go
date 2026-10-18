// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// oneShotLimiter admits the first two calls per key -- a claim's begin and
// complete share their barrier's bucket -- and refuses every later one,
// recording what it was asked.
type oneShotLimiter struct {
	seen      map[string]int
	anonymous []bool
}

func (l *oneShotLimiter) allow(key string, anonymous bool) (bool, time.Duration) {
	if l.seen == nil {
		l.seen = map[string]int{}
	}
	l.seen[key]++
	l.anonymous = append(l.anonymous, anonymous)
	return l.seen[key] <= 2, time.Second
}

// recordingDBHolders serves the database claim calls.
type recordingDBHolders struct {
	service.DatabaseServicer
	service.DatabaseHolderServicer
	calls int
}

func (r *recordingDBHolders) DatabaseBarrierShareClaimBegin(context.Context, string, string, string) (*service.BarrierShareClaimChallenge, error) {
	r.calls++
	return &service.BarrierShareClaimChallenge{}, nil
}

func (r *recordingDBHolders) DatabaseBarrierShareClaimComplete(context.Context, string, string, string, []byte, string) (*service.BarrierShareClaimResult, error) {
	r.calls++
	return &service.BarrierShareClaimResult{}, nil
}

// TestHolderActLimiter_ThrottlesAnonymousClaims: a claim with no identity is
// charged to its scope's bucket, and one over the throttle is refused before
// the service sees it -- on every scope, for begin and complete alike.
func TestHolderActLimiter_ThrottlesAnonymousClaims(t *testing.T) {
	lim := &oneShotLimiter{}
	barrier := &recordingClaimBarrier{}
	tenantSvc := &recordingClaimTenant{}
	db := &recordingDBHolders{}
	tr := newBarrierTransport(t, barrier)
	tr.holderActLimiter = lim.allow
	tenantTr, err := New(newMockKVService(), &mockAdminService{}, WithTenantService(tenantSvc), WithHolderActLimiter(lim.allow))
	if err != nil {
		t.Fatal(err)
	}
	dbTr, err := New(newMockKVService(), &mockAdminService{}, WithDatabaseService(db), WithHolderActLimiter(lim.allow))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	steps := map[string]func() error{
		"ShareClaimBegin": func() error {
			_, err := tr.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "carol"})
			return err
		},
		"ShareClaimComplete": func() error {
			_, err := tr.ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: "carol"})
			return err
		},
		"RecoveryShareClaimBegin": func() error {
			_, err := tr.RecoveryShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "carol"})
			return err
		},
		"RecoveryShareClaimComplete": func() error {
			_, err := tr.RecoveryShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: "carol"})
			return err
		},
		"TenantBarrierShareClaimBegin": func() error {
			_, err := tenantTr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"TenantBarrierShareClaimComplete": func() error {
			_, err := tenantTr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"DatabaseBarrierShareClaimBegin": func() error {
			_, err := dbTr.DatabaseBarrierShareClaimBegin(ctx, &transport.DatabaseShareClaimBeginRequest{Database: "vault", ShareClaimBeginRequest: transport.ShareClaimBeginRequest{Username: "carol"}})
			return err
		},
		"DatabaseBarrierShareClaimComplete": func() error {
			_, err := dbTr.DatabaseBarrierShareClaimComplete(ctx, &transport.DatabaseShareClaimCompleteRequest{Database: "vault", ShareClaimCompleteRequest: transport.ShareClaimCompleteRequest{Username: "carol"}})
			return err
		},
	}
	for name, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("%s: first claim = %v, want admitted", name, err)
		}
	}
	served := len(barrier.calls) + len(tenantSvc.calls) + db.calls
	for name, step := range steps {
		err := step()
		var limited *HolderActRateLimitedError
		if !errors.As(err, &limited) {
			t.Fatalf("%s: second claim = %v (%T), want *HolderActRateLimitedError", name, err, err)
		}
	}
	if after := len(barrier.calls) + len(tenantSvc.calls) + db.calls; after != served {
		t.Fatalf("a throttled claim reached the service: %d calls, want %d", after, served)
	}
	for _, anon := range lim.anonymous {
		if !anon {
			t.Fatal("an identity-less claim was charged as an identified caller")
		}
	}
}

// TestHolderActLimiter_IdentifiedClaimsAreNotChargedHere: a claim carrying
// an identity is the auth middleware's to rate-limit, not the anonymous
// bucket's -- requireIdentity authorizes it, and limitHolderAct skips a
// call whose context already carries an identity.
func TestHolderActLimiter_IdentifiedClaimsAreNotChargedHere(t *testing.T) {
	lim := &oneShotLimiter{}
	tr := newBarrierTransport(t, &recordingClaimBarrier{})
	tr.holderActLimiter = lim.allow
	tr.authMW = authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "carol", TenantID: tenantid.System})
	for i := 0; i < 4; i++ {
		if _, err := tr.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "carol"}); err != nil {
			t.Fatalf("identified claim %d = %v", i, err)
		}
	}
	if len(lim.seen) != 0 {
		t.Fatalf("identified claims charged the anonymous bucket: %v", lim.seen)
	}
}

// TestHolderActLimiter_SystemTenantHasOneBucket: a tenant claim naming ""
// and one naming the system tenant are the same barrier, so they share a
// bucket.
func TestHolderActLimiter_SystemTenantHasOneBucket(t *testing.T) {
	lim := &oneShotLimiter{}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithTenantService(&recordingClaimTenant{}), WithHolderActLimiter(lim.allow))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", tenantid.System} {
		if _, err := tr.TenantBarrierShareClaimBegin(context.Background(), &transport.TenantShareClaimBeginRequest{TenantID: id, Username: "carol"}); err != nil {
			t.Fatalf("claim for %q: %v", id, err)
		}
	}
	if len(lim.seen) != 1 {
		t.Fatalf("the system tenant was charged to %d buckets (%v), want 1", len(lim.seen), lim.seen)
	}
}
