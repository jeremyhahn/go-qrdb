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

package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// noHolderDatabaseSvc is a database service without share-holder custody.
// The tools only type-assert it; none of its methods is called.
type noHolderDatabaseSvc struct{ service.DatabaseServicer }

// dbHolderTools is the database share-holder tool set over svc, called by
// name with a request the SDK encodes the same way.
type dbHolderTools struct {
	t     *testing.T
	tools []serverTool
}

// call invokes tool with req's arguments and decodes its JSON result into
// out. It returns the tool's error, or an error result as a plain error.
func (d dbHolderTools) call(ctx context.Context, tool string, req, out any) error {
	d.t.Helper()
	args, err := wire.EncodeArgs(req, wire.HolderJSONFields...)
	if err != nil {
		d.t.Fatal(err)
	}
	res, err := holderTool(d.t, d.tools, tool)(ctx, buildCallToolRequest(d.t, tool, args))
	if err != nil {
		return err
	}
	text := resultText(d.t, res)
	if res.IsError {
		return errors.New(text)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		d.t.Fatalf("%s: decode %q: %v", tool, text, err)
	}
	return nil
}

// dbHolderTLSContext is a context whose session presents h's certificate.
func dbHolderTLSContext(h *servicetest.Holder) context.Context {
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{h.Cert}})
}

// submitMCPDBShare answers h's mtls challenge for op and submits its share.
func submitMCPDBShare(t *testing.T, d dbHolderTools, op string, h *servicetest.Holder) *transport.QuorumOutcome {
	t.Helper()
	ctx := context.Background()
	var ch transport.QuorumChallenge
	if err := d.call(ctx, "qrdb_database_barrier_quorum_begin", transport.DatabaseQuorumBeginRequest{
		Database: "vault", QuorumBeginRequest: transport.QuorumBeginRequest{Op: op, Username: h.Name},
	}, &ch); err != nil {
		t.Fatalf("%s begin %s: %v", op, h.Name, err)
	}
	var out transport.QuorumOutcome
	if err := d.call(ctx, "qrdb_database_barrier_quorum_submit", transport.DatabaseQuorumSubmitRequest{
		Database: "vault",
		QuorumSubmitRequest: transport.QuorumSubmitRequest{
			Op: op, Username: h.Name, ChallengeID: ch.ChallengeID, Factor: ch.Factor,
			Response: h.Sign(t, ch.Nonce), Share: h.Share(t),
		},
	}, &out); err != nil {
		t.Fatalf("%s submit %s: %v", op, h.Name, err)
	}
	return &out
}

// TestDatabaseHolderTools_Lifecycle drives a Shamir database's custody
// through the qrdb_database_barrier_* tools over a real database service:
// enrolment, listing, initialization, claims presenting each holder's
// certificate on the session, a quorum seal and unseal, a rekey round and a
// revocation the remaining holders cannot satisfy.
func TestDatabaseHolderTools_Lifecycle(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	d := dbHolderTools{t: t, tools: databaseHolderTools(fx.Service)}
	ctx := context.Background()
	holders := []*servicetest.Holder{servicetest.NewHolder(t, "alice"), servicetest.NewHolder(t, "bob")}

	for _, h := range holders {
		var resp transport.OfficerRegisterResponse
		if err := d.call(ctx, "qrdb_database_barrier_officer_register", transport.DatabaseOfficerRegisterRequest{
			Database: "vault", OfficerRegisterRequest: transport.OfficerRegisterRequest{Username: h.Name, CSRPEM: h.CSRPEM},
		}, &resp); err != nil {
			t.Fatalf("register %s: %v", h.Name, err)
		}
		h.Enrolled(t, resp.CertPEM)
	}
	var list struct {
		Officers []transport.OfficerStatus `json:"officers"`
	}
	if err := d.call(ctx, "qrdb_database_barrier_officer_list", map[string]string{"database": "vault"}, &list); err != nil || len(list.Officers) != 2 {
		t.Fatalf("list = %v, %v", list, err)
	}
	var init transport.BarrierInitializeShamirResponse
	if err := d.call(ctx, "qrdb_database_barrier_shamir_initialize", transport.DatabaseShamirInitializeRequest{Database: "vault"}, &init); err != nil || init.ShareCount != 2 {
		t.Fatalf("initialize = %+v, %v", init, err)
	}

	for _, h := range holders {
		beginReq := transport.DatabaseShareClaimBeginRequest{Database: "vault", ShareClaimBeginRequest: transport.ShareClaimBeginRequest{Username: h.Name}}
		var begin transport.ShareClaimBeginResponse
		var noCert *service.ClientCertRequiredError
		if err := d.call(ctx, "qrdb_database_barrier_share_claim_begin", beginReq, &begin); !errors.As(err, &noCert) {
			t.Fatalf("claim begin without a certificate: got %v, want ClientCertRequiredError", err)
		}
		if err := d.call(ctx, "qrdb_database_barrier_share_claim_complete", transport.DatabaseShareClaimCompleteRequest{Database: "vault"}, &struct{}{}); !errors.As(err, &noCert) {
			t.Fatalf("claim complete without a certificate: got %v, want ClientCertRequiredError", err)
		}
		tlsCtx := dbHolderTLSContext(h)
		if err := d.call(tlsCtx, "qrdb_database_barrier_share_claim_begin", beginReq, &begin); err != nil {
			t.Fatalf("claim begin %s: %v", h.Name, err)
		}
		var done transport.ShareClaimCompleteResponse
		if err := d.call(tlsCtx, "qrdb_database_barrier_share_claim_complete", transport.DatabaseShareClaimCompleteRequest{
			Database: "vault",
			ShareClaimCompleteRequest: transport.ShareClaimCompleteRequest{
				Username: h.Name, NonceHex: begin.NonceHex, Signature: h.SignClaimHex(t, "vault", begin.NonceHex),
			},
		}, &done); err != nil {
			t.Fatalf("claim complete %s: %v", h.Name, err)
		}
		h.Envelope = done.Envelope
	}

	for _, op := range []string{"seal", "unseal"} {
		submitMCPDBShare(t, d, op, holders[0])
		if out := submitMCPDBShare(t, d, op, holders[1]); !out.Complete {
			t.Fatalf("%s quorum = %+v", op, out)
		}
	}

	var round transport.QuorumRound
	if err := d.call(ctx, "qrdb_database_barrier_rekey_start", transport.DatabaseRekeyStartRequest{Database: "vault", Threshold: 2}, &round); err != nil || round.Op != "rekey" {
		t.Fatalf("rekey start = %+v, %v", round, err)
	}
	if err := d.call(ctx, "qrdb_database_barrier_officer_revoke", transport.DatabaseOfficerRevokeRequest{Database: "vault", Username: "bob", Threshold: 2}, &round); err == nil {
		t.Fatal("revoking a holder below the threshold succeeded")
	}
}

// TestDatabaseHolderTools_Refusals covers the tools' own refusals: no
// database named, arguments that do not decode, a database without a Shamir
// barrier, and a database service without share-holder custody.
func TestDatabaseHolderTools_Refusals(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	d := dbHolderTools{t: t, tools: databaseHolderTools(fx.Service)}
	bare := dbHolderTools{t: t, tools: databaseHolderTools(noHolderDatabaseSvc{})}
	ctx := context.Background()

	names := []string{
		"qrdb_database_barrier_officer_register", "qrdb_database_barrier_officer_list",
		"qrdb_database_barrier_shamir_initialize", "qrdb_database_barrier_share_claim_begin",
		"qrdb_database_barrier_share_claim_complete", "qrdb_database_barrier_quorum_begin",
		"qrdb_database_barrier_quorum_submit", "qrdb_database_barrier_rekey_start",
		"qrdb_database_barrier_officer_revoke",
	}
	for _, name := range names {
		if err := d.call(ctx, name, map[string]string{}, &struct{}{}); err == nil || err.Error() != "database is required" {
			t.Errorf("%s naming no database: got %v, want the required-argument error", name, err)
		}
		var notConfigured *ServiceNotConfiguredError
		if err := bare.call(ctx, name, map[string]string{"database": "vault"}, &struct{}{}); !errors.As(err, &notConfigured) {
			t.Errorf("%s without holder custody: got %v, want ServiceNotConfiguredError", name, err)
		}
		if name == "qrdb_database_barrier_share_claim_begin" || name == "qrdb_database_barrier_share_claim_complete" {
			continue
		}
		if err := d.call(ctx, name, map[string]any{"database": "absent", "threshold": 2, "op": "unseal"}, &struct{}{}); err == nil {
			t.Errorf("%s on a database without a Shamir barrier succeeded", name)
		}
	}
	res, err := holderTool(t, d.tools, "qrdb_database_barrier_officer_register")(ctx, buildCallToolRequest(t, "qrdb_database_barrier_officer_register", map[string]any{"database": "vault", "factors": "{"}))
	if err == nil {
		t.Fatalf("undecodable factors were accepted: %+v", res)
	}
}
