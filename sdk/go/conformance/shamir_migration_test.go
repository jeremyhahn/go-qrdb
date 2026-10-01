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

package conformance

import (
	"encoding/hex"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestShareHolders_SystemMigrationToShamirKeepsTheEnvelopes proves a
// system-scope encryption migration to Shamir leaves the barrier openable.
// The migration distributes the new envelopes through the node's ceremony
// service and then restarts the data plane in process; the restart must carry
// that ceremony service across, or the envelopes are lost and the sealed
// barrier can never be opened. Here the enrolled holders claim their
// envelopes from the sealed node, a quorum unseals it, and the data written
// before the migration is intact.
func TestShareHolders_SystemMigrationToShamirKeepsTheEnvelopes(t *testing.T) {
	e := newHolderE2E(t, embeddedTransport)
	e.cfg.Seal.Strategy = "none"
	e.boot()
	if e.n.IsSealed() {
		t.Fatal("a none-strategy node must boot unsealed")
	}
	ctx := boundedCtx(t)
	svc := e.n.Services().Barrier
	for _, name := range e.names() {
		h := e.holders[name]
		res, err := svc.BarrierOfficerRegister(ctx, seal.DomainUnseal, holderwire.RegisterFrom(h.registerRequest()))
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		h.recordCert(t, holderwire.RegisterTo(res).CertPEM)
	}
	if err := e.n.KVService().Put(ctx, e2eKVKey, []byte(e2eKVValue)); err != nil {
		t.Fatal(err)
	}

	if err := e.n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: e2eThreshold,
		ShamirTotal:     e2eTotal,
	}); err != nil {
		t.Fatalf("enable encryption to shamir: %v", err)
	}
	if !e.n.IsSealed() {
		t.Fatal("a node migrated to Shamir must come back sealed")
	}

	// The holders claim from the sealed node's own services.
	for _, name := range e.names() {
		h := e.holders[name]
		api, hctx := e.holderClient(h, true)
		begin, err := api.ShareClaimBegin(hctx, &transport.ShareClaimBeginRequest{Username: h.name})
		if err != nil {
			t.Fatalf("claim begin %s after the migration: %v", name, err)
		}
		nonce, err := hex.DecodeString(begin.NonceHex)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := sdk.SignShareClaim(h.key, sdk.SystemClaimLabel, h.name, nonce)
		if err != nil {
			t.Fatal(err)
		}
		done, err := api.ShareClaimComplete(hctx, &transport.ShareClaimCompleteRequest{Username: h.name, NonceHex: begin.NonceHex, Signature: sig})
		if err != nil {
			t.Fatalf("claim complete %s after the migration: %v", name, err)
		}
		h.envelope = done.Envelope
	}

	var out *transport.QuorumOutcome
	for _, name := range []string{"cust-carol", "cust-dave", "so-alice"} {
		out = e.mustSubmit(name, transport.OpUnseal, "", true)
	}
	if !out.Complete {
		t.Fatalf("the unseal quorum did not complete: %+v", out)
	}
	if err := e.n.Unseal(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("complete the boot: %v", err)
	}
	got, err := e.n.KVService().Get(ctx, e2eKVKey)
	if err != nil || string(got) != e2eKVValue {
		t.Fatalf("data after the migration and unseal = %q, %v; want %q", got, err, e2eKVValue)
	}
}
