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

package service

import (
	"encoding/hex"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestDatabaseHolders_RekeyOutcomeSurvivesAFailedSchemeRecord: a database
// rekey that reaches its quorum has completed. The barrier holds the new key
// and the holders hold the new shares, and their old shares are dead. Writing
// the new M-of-N into the database's catalog record comes after that and can
// fail. It used to turn the completed rekey into an error, so the last holder
// was told their submission failed when it had in fact retired every old
// share. The outcome is now returned and the failure logged.
func TestDatabaseHolders_RekeyOutcomeSurvivesAFailedSchemeRecord(t *testing.T) {
	svc, mgr := newHolderDatabaseService(t)
	ctx := dbAdminCtx("acme")
	holders := []*dbSvcHolder{newDBSvcHolder(t, "alice"), newDBSvcHolder(t, "bob")}
	for _, h := range holders {
		res, err := svc.DatabaseBarrierOfficerRegister(ctx, "vault", &BarrierOfficerRegisterRequest{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr})
		if err != nil {
			t.Fatal(err)
		}
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		if err != nil {
			t.Fatal(err)
		}
		h.thumb = mtls.Thumbprint(cert.Raw)
	}
	if _, err := svc.DatabaseBarrierShamirInitialize(ctx, "vault", ""); err != nil {
		t.Fatal(err)
	}
	for _, h := range holders {
		ch, err := svc.DatabaseBarrierShareClaimBegin(ctx, "vault", h.name, h.thumb)
		if err != nil {
			t.Fatal(err)
		}
		nonce, err := hex.DecodeString(ch.NonceHex)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := ceremony.SignShareClaim(h.key, ceremony.DatabaseClaimLabel("vault"), h.name, nonce)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.DatabaseBarrierShareClaimComplete(ctx, "vault", h.name, ch.NonceHex, sig, h.thumb)
		if err != nil {
			t.Fatal(err)
		}
		h.envelope = res.Envelope
	}
	if _, err := svc.DatabaseBarrierRekeyStart(ctx, "vault", 2, ""); err != nil {
		t.Fatal(err)
	}

	// The catalog record cannot be written: the catalog refuses every Put.
	mgr.SetCatalog(putRefusingCatalog{Catalog: databasetest.Catalog(mgr.DataDir())})

	if _, err := submitDBSvc(t, svc, ctx, seal.OpRekey, holders[0]); err != nil {
		t.Fatal(err)
	}
	out, err := submitDBSvc(t, svc, ctx, seal.OpRekey, holders[1])
	if err != nil {
		t.Fatalf("the quorum's last submission = %v; the rekey completed and must be reported", err)
	}
	if !out.Complete || out.Rekey == nil || out.Rekey.TotalShares != 2 {
		t.Fatalf("rekey outcome = %+v", out)
	}
}
