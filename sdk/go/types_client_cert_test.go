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

package sdk_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// ocspCA is an embedder's login CA written against the SDK aliases alone: a
// ClientCertProvider that is also a ClientCertSource, so the node's
// client-trust pool admits what it issues.
type ocspCA struct {
	ca      *x509.Certificate
	tenant  string
	revoked map[string]bool
}

var (
	_ sdk.ClientCertProvider           = (*ocspCA)(nil)
	_ sdk.ClientCertSource             = (*ocspCA)(nil)
	_ sdk.ClientCertInvalidationSource = (*ocspCA)(nil)
)

var errNotIssuing = errors.New("ocspCA: issuance is out of this test's scope")

func (c *ocspCA) IssueClientCert(context.Context, *sdk.ClientCertIssueRequest) (*sdk.ClientCertIssued, error) {
	return nil, errNotIssuing
}

func (c *ocspCA) RevokeCert(_ context.Context, serial string) (*sdk.ClientCertRevokeResult, error) {
	c.revoked[serial] = true
	return &sdk.ClientCertRevokeResult{Serial: serial, Outcome: sdk.ClientCertRevokeDenyBySerial}, nil
}

func (c *ocspCA) IsRevoked(_ context.Context, serial string) (bool, error) {
	return c.revoked[serial], nil
}

func (c *ocspCA) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (c *ocspCA) ListCerts(context.Context, string) ([]*sdk.ClientCertRecord, error) {
	return nil, nil
}

func (c *ocspCA) CACertPEMs(context.Context) ([][]byte, error) {
	return [][]byte{pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.ca.Raw})}, nil
}

func (c *ocspCA) IsTenantIssuer(_ context.Context, tenantID string, issuer *x509.Certificate) (bool, error) {
	return tenantID == c.tenant && issuer.Equal(c.ca), nil
}

func (c *ocspCA) OnInvalidate(func()) {}

func newOCSPCA(t *testing.T) *ocspCA {
	_, ca := selfSignedCA(t, "acme login CA")
	return &ocspCA{ca: ca, tenant: "acme", revoked: map[string]bool{}}
}

func TestSDK_ClientCertProviderRevokesBySerial(t *testing.T) {
	var p sdk.ClientCertProvider = newOCSPCA(t)
	ctx := context.Background()
	res, err := p.RevokeCert(ctx, "0a")
	if err != nil {
		t.Fatal(err)
	}
	if res.RecordFound() || res.Outcome != sdk.ClientCertRevokeDenyBySerial {
		t.Fatalf("RevokeCert = %+v, want a deny-by-serial result", res)
	}
	if revoked, _ := p.IsRevoked(ctx, "0a"); !revoked {
		t.Fatal("the revoked serial is not reported revoked")
	}
	if revoked, _ := p.IsRevoked(ctx, "0b"); revoked {
		t.Fatal("an unrevoked serial is reported revoked")
	}
}

func TestSDK_ClientCertSourceBindsTheIssuerToItsTenant(t *testing.T) {
	ca := newOCSPCA(t)
	var src sdk.ClientCertSource = ca
	ctx := context.Background()
	if ok, _ := src.IsTenantIssuer(ctx, "acme", ca.ca); !ok {
		t.Fatal("the tenant's own CA is not its issuer")
	}
	if ok, _ := src.IsTenantIssuer(ctx, "globex", ca.ca); ok {
		t.Fatal("one tenant's CA was accepted as another tenant's issuer")
	}
	pems, err := src.CACertPEMs(ctx)
	if err != nil || len(pems) != 1 {
		t.Fatalf("CACertPEMs = %d bundles, %v", len(pems), err)
	}
}

func TestSDK_ClientCertRevokeOutcomesRender(t *testing.T) {
	if sdk.ClientCertRevokeRecordUpdated.String() == sdk.ClientCertRevokeDenyBySerial.String() {
		t.Fatal("the two revocation outcomes render alike")
	}
	if !(&sdk.ClientCertRevokeResult{Outcome: sdk.ClientCertRevokeRecordUpdated}).RecordFound() {
		t.Fatal("a record-updated result does not report its record")
	}
}
