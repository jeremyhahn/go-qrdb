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

// Package dbholdertest is the client-side conformance suite of a database
// barrier's Shamir custody (docs/databases). Every SDK transport runs it
// against a real database service, so the six clients are held to one
// behaviour and cannot drift.
//
// It is imported by tests only; a production import of it is a defect.
package dbholdertest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Database is the name of the suite's 2-of-2 Shamir database.
const Database = "vault"

// NewFixture returns the real database service the suite expects the
// transport under test to serve: Database, 2-of-2, holders not yet enrolled.
func NewFixture(t testing.TB) *servicetest.ShamirDatabase {
	t.Helper()
	return servicetest.NewShamirDatabase(t, Database, 2, 2)
}

// Claim says how the transport under test claims a share.
type Claim struct {
	// refused reports whether err is the transport's refusal of a claim
	// made without the holder's certificate; nil for an in-process claim.
	refused func(err error) bool
}

// ClaimInProcess is the embedded transport: the claim is bound to the
// enrolled certificate by a signature over the server's nonce, so the suite
// claims through the transport.
var ClaimInProcess = Claim{}

// ClaimRefusedWithStatus is a network transport the suite drives without the
// holder's certificate: its claim must be refused as unauthenticated, with
// status, and the suite then claims through the service directly. Any other
// failure -- an unrouted call, say -- does not count as the refusal.
func ClaimRefusedWithStatus(status int) Claim {
	return Claim{refused: func(err error) bool {
		// Every *transport.RequestError in the chain is checked, not only the
		// outermost: a transport may wrap the one carrying the status.
		for err != nil {
			var re *transport.RequestError
			if !errors.As(err, &re) {
				return false
			}
			if re.StatusCode == status {
				return true
			}
			err = re.Unwrap()
		}
		return false
	}}
}

// Run drives fx's database through c: enrolment of two holders, listing,
// initialization at the created M-of-N, each holder's claim, a quorum seal
// and unseal, a rekey round, a revocation the remaining holders cannot
// satisfy, and the client's refusal of a request that names no database.
func Run(t *testing.T, c transport.DatabaseClient, fx *servicetest.ShamirDatabase, claim Claim) {
	t.Helper()
	ctx := context.Background()
	holders := []*servicetest.Holder{servicetest.NewHolder(t, "alice"), servicetest.NewHolder(t, "bob")}

	for _, h := range holders {
		resp, err := c.DatabaseBarrierOfficerRegister(ctx, &transport.DatabaseOfficerRegisterRequest{
			Database:               Database,
			OfficerRegisterRequest: transport.OfficerRegisterRequest{Username: h.Name, CSRPEM: h.CSRPEM},
		})
		if err != nil {
			t.Fatalf("register %s: %v", h.Name, err)
		}
		h.Enrolled(t, resp.CertPEM)
	}

	officers, err := c.DatabaseBarrierOfficerList(ctx, Database)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(officers) != 2 {
		t.Fatalf("listed %d holders, want 2", len(officers))
	}

	init, err := c.DatabaseBarrierShamirInitialize(ctx, &transport.DatabaseShamirInitializeRequest{Database: Database})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if init.Threshold != 2 || init.TotalShares != 2 || init.ShareCount != 2 {
		t.Fatalf("initialize = %+v, want 2-of-2 with 2 shares", init)
	}

	for _, h := range holders {
		claimShare(t, c, fx, h, claim)
	}

	for _, op := range []string{"seal", "unseal"} {
		var out *transport.QuorumOutcome
		for _, h := range holders {
			out = submit(t, c, op, h)
		}
		if !out.Complete {
			t.Fatalf("%s quorum did not complete: %+v", op, out)
		}
	}

	round, err := c.DatabaseBarrierRekeyStart(ctx, &transport.DatabaseRekeyStartRequest{Database: Database, Threshold: 2})
	if err != nil {
		t.Fatalf("rekey start: %v", err)
	}
	if round.Op != "rekey" {
		t.Fatalf("rekey round = %+v", round)
	}
	if _, err := c.DatabaseBarrierOfficerRevoke(ctx, &transport.DatabaseOfficerRevokeRequest{Database: Database, Username: "bob", Threshold: 2}); err == nil {
		t.Fatal("revoking a holder below the threshold succeeded")
	}

	refusesNoDatabase(t, c)
}

// claimShare claims h's share as the transport's claim mode allows.
func claimShare(t *testing.T, c transport.DatabaseClient, fx *servicetest.ShamirDatabase, h *servicetest.Holder, claim Claim) {
	t.Helper()
	ctx := context.Background()
	begin, err := c.DatabaseBarrierShareClaimBegin(ctx, &transport.DatabaseShareClaimBeginRequest{
		Database: Database, ShareClaimBeginRequest: transport.ShareClaimBeginRequest{Username: h.Name},
	})
	if claim.refused != nil {
		if !claim.refused(err) {
			t.Fatalf("claim begin for %s without the holder's certificate: got %v, want the unauthenticated refusal", h.Name, err)
		}
		fx.Claim(t, h)
		return
	}
	if err != nil {
		t.Fatalf("claim begin %s: %v", h.Name, err)
	}
	done, err := c.DatabaseBarrierShareClaimComplete(ctx, &transport.DatabaseShareClaimCompleteRequest{
		Database: Database,
		ShareClaimCompleteRequest: transport.ShareClaimCompleteRequest{
			Username: h.Name, NonceHex: begin.NonceHex, Signature: h.SignHex(t, begin.NonceHex),
		},
	})
	if err != nil {
		t.Fatalf("claim complete %s: %v", h.Name, err)
	}
	h.Envelope = done.Envelope
}

// submit answers h's mtls challenge for op and submits its share.
func submit(t *testing.T, c transport.DatabaseClient, op string, h *servicetest.Holder) *transport.QuorumOutcome {
	t.Helper()
	ctx := context.Background()
	ch, err := c.DatabaseBarrierQuorumBegin(ctx, &transport.DatabaseQuorumBeginRequest{
		Database: Database, QuorumBeginRequest: transport.QuorumBeginRequest{Op: op, Username: h.Name},
	})
	if err != nil {
		t.Fatalf("%s quorum begin %s: %v", op, h.Name, err)
	}
	out, err := c.DatabaseBarrierQuorumSubmit(ctx, &transport.DatabaseQuorumSubmitRequest{
		Database: Database,
		QuorumSubmitRequest: transport.QuorumSubmitRequest{
			Op: op, Username: h.Name, ChallengeID: ch.ChallengeID, Factor: ch.Factor,
			Response: h.Sign(t, ch.Nonce), Share: h.Share(t),
		},
	})
	if err != nil {
		t.Fatalf("%s quorum submit %s: %v", op, h.Name, err)
	}
	return out
}

// refusesNoDatabase asserts every call refuses a nil request or an empty
// database name before it reaches the server.
func refusesNoDatabase(t *testing.T, c transport.DatabaseClient) {
	t.Helper()
	ctx := context.Background()
	calls := map[string]func() error{
		"OfficerRegister":    func() error { _, err := c.DatabaseBarrierOfficerRegister(ctx, nil); return err },
		"OfficerList":        func() error { _, err := c.DatabaseBarrierOfficerList(ctx, ""); return err },
		"ShamirInitialize":   func() error { _, err := c.DatabaseBarrierShamirInitialize(ctx, nil); return err },
		"ShareClaimBegin":    func() error { _, err := c.DatabaseBarrierShareClaimBegin(ctx, nil); return err },
		"ShareClaimComplete": func() error { _, err := c.DatabaseBarrierShareClaimComplete(ctx, nil); return err },
		"QuorumBegin":        func() error { _, err := c.DatabaseBarrierQuorumBegin(ctx, nil); return err },
		"QuorumSubmit":       func() error { _, err := c.DatabaseBarrierQuorumSubmit(ctx, nil); return err },
		"RekeyStart": func() error {
			_, err := c.DatabaseBarrierRekeyStart(ctx, &transport.DatabaseRekeyStartRequest{})
			return err
		},
		"OfficerRevoke": func() error { _, err := c.DatabaseBarrierOfficerRevoke(ctx, nil); return err },
	}
	for name, call := range calls {
		var required *transport.DatabaseNameRequiredError
		if err := call(); !errors.As(err, &required) {
			t.Errorf("%s with no database: got %v, want DatabaseNameRequiredError", name, err)
		}
	}
}
