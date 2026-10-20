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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestWithPreparedShamir_BothEmptyIsTheFirstRequest(t *testing.T) {
	var o tenantBarrierInitOptions
	WithPreparedShamir(nil, nil)(&o)
	WithPreparedShamir([]byte{}, []byte{})(&o)
	if o.shamirSplit != nil {
		t.Fatalf("shamirSplit = %+v, want none for an empty echo", o.shamirSplit)
	}
}

func TestWithPreparedShamir_KeepsTheEchoedSplit(t *testing.T) {
	for name, split := range map[string][2][]byte{
		"both":        {[]byte("roster"), []byte("check")},
		"roster only": {[]byte("roster"), nil},
		"check only":  {nil, []byte("check")},
	} {
		var o tenantBarrierInitOptions
		WithPreparedShamir(split[0], split[1])(&o)
		if o.shamirSplit == nil || string(o.shamirSplit.roster) != string(split[0]) || string(o.shamirSplit.check) != string(split[1]) {
			t.Errorf("%s: shamirSplit = %+v, want the echoed split", name, o.shamirSplit)
		}
	}
}

// prepareShamir runs the first request of acme's Shamir init and returns
// the split it prepared.
func prepareShamir(t *testing.T, svc *TenantService, ctx context.Context, tenantID string) *seal.ShamirInitParams {
	t.Helper()
	first, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, nil, 2, 2, seal.StrategyShamir, shamirClaimRoster(t))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	return &seal.ShamirInitParams{Threshold: first.Threshold, Roster: first.Roster, UnsealKeyCheck: first.UnsealKeyCheck}
}

// A tenant whose init needs no statement completes on the echoed split alone.
func TestTenantBarrierInit_ShamirEchoedSplitCompletesWithoutAStatement(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	p := prepareShamir(t, svc, ctx, "acme")

	second, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
		WithPreparedShamir(p.Roster, p.UnsealKeyCheck))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if !initialized(t, svc, "acme") || len(book.claimed()) != 0 {
		t.Fatalf("initialized = %v, claims = %+v, want a keyed barrier and no claim", initialized(t, svc, "acme"), book.claimed())
	}
	if string(second.Roster) != string(p.Roster) || string(second.UnsealKeyCheck) != string(p.UnsealKeyCheck) {
		t.Fatal("the second request returned another split than the first")
	}
}

func TestTenantBarrierInit_ShamirEchoedSplitWithItsStatementIsClaimed(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	p := prepareShamir(t, svc, ctx, "acme")
	statement := shamirStatement(p)

	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
		WithInitStatement(statement), WithPreparedShamir(p.Roster, p.UnsealKeyCheck)); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if got := book.claimed(); len(got) != 1 || got[0].statement != string(statement) || !initialized(t, svc, "acme") {
		t.Fatalf("claims = %+v, want the statement claimed and the barrier keyed", got)
	}
}

// An echo of another split is refused before the owner's statement is
// claimed, so the right echo still completes the init afterwards.
func TestTenantBarrierInit_ShamirMismatchedEchoIsRefusedBeforeTheClaim(t *testing.T) {
	for name, c := range map[string]struct {
		field  string
		mutate func(roster, check []byte) ([]byte, []byte)
	}{
		"roster": {"roster", func(r, c []byte) ([]byte, []byte) { return append(append([]byte{}, r...), 'x'), c }},
		"check":  {"unseal key check", func(r, c []byte) ([]byte, []byte) { return r, append([]byte{^c[0]}, c[1:]...) }},
	} {
		t.Run(name, func(t *testing.T) {
			book := &bookDouble{}
			svc, ctx := newShamirClaimService(t, book, "acme")
			p := prepareShamir(t, svc, ctx, "acme")
			statement := shamirStatement(p)

			roster, check := c.mutate(p.Roster, p.UnsealKeyCheck)
			_, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
				WithInitStatement(statement), WithPreparedShamir(roster, check))
			var mismatch *seal.PreparedShamirMismatchError
			if !errors.As(err, &mismatch) || mismatch.Field != c.field {
				t.Fatalf("mismatched echo = %v, want PreparedShamirMismatchError{%q}", err, c.field)
			}
			if initialized(t, svc, "acme") || len(book.claimed()) != 0 {
				t.Fatal("a mismatched echo keyed the barrier or spent the statement")
			}
			if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
				WithInitStatement(statement), WithPreparedShamir(p.Roster, p.UnsealKeyCheck)); err != nil {
				t.Fatalf("the right echo after a refused one: %v", err)
			}
		})
	}
}

// A raft tenant's init needs the owner's statement: the echo alone is refused.
func TestTenantBarrierInit_ShamirRaftTenantEchoWithoutAStatementIsRefused(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	p := prepareShamir(t, svc, ctx, "acme")

	var opts tenantBarrierInitOptions
	WithPreparedShamir(p.Roster, p.UnsealKeyCheck)(&opts)
	_, err := svc.completeTenantShamir(ctx, &TenantInfo{ID: "acme", IsolationType: IsolationRaft}, opts)
	var required *InitStatementRequiredError
	if !errors.As(err, &required) || required.TenantID != "acme" {
		t.Fatalf("raft tenant echo without a statement = %v, want InitStatementRequiredError", err)
	}
	if initialized(t, svc, "acme") || len(book.claimed()) != 0 {
		t.Fatal("the barrier was keyed without the owner's statement")
	}
}
