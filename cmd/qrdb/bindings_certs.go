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

package main

import (
	"context"
	"encoding/base64"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CertBinding exposes certificate management operations to the Wails frontend.
type CertBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewCertBinding creates a new CertBinding backed by the given connection.
func NewCertBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *CertBinding {
	return &CertBinding{logger: log, conn: conn}
}

// IssueCertInput asks the desktop to issue a certificate for an existing
// account of the connected tenant. With CSRPEM empty, the key is generated
// HERE, on this machine, and returned beside the certificate for the user to
// keep; the server only ever sees a CSR.
type IssueCertInput struct {
	Username string `json:"username"`
	CSRPEM   string `json:"csrPEM,omitempty"`
	TTL      string `json:"ttl,omitempty"`
}

// IssuedCertOutput is an issued certificate, bound to its account, and the
// key generated for it on this machine when the input carried no CSR.
type IssuedCertOutput struct {
	transport.EnrolledCert
	KeyPEM string `json:"keyPEM,omitempty"`
}

// Issue issues a certificate, signed by the tenant's own CA, for an existing
// account of the connected tenant, and binds it.
func (b *CertBinding) Issue(in *IssueCertInput) (*IssuedCertOutput, error) {
	b.logger.Debug("Issue called")
	csrPEM := in.CSRPEM
	var keyPEM []byte
	if csrPEM == "" {
		k, c, err := newClientKeyAndCSR(in.Username)
		if err != nil {
			b.logger.Error("Issue failed", "error", err)
			return nil, err
		}
		keyPEM, csrPEM = k, string(c)
	}
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Issue failed", "error", err)
		return nil, err
	}
	issued, err := cl.Certs().IssueCert(context.Background(), &transport.IssueCertRequest{
		Username: in.Username,
		CSRPEM:   csrPEM,
		TTL:      in.TTL,
	})
	if err != nil {
		b.logger.Error("Issue failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Issue completed")
	return &IssuedCertOutput{EnrolledCert: *issued, KeyPEM: string(keyPEM)}, nil
}

// Enroll binds a bring-your-own certificate, which must chain to one of the
// tenant's trust anchors, to an existing account of the connected tenant.
func (b *CertBinding) Enroll(req *transport.EnrollCertRequest) (*transport.EnrolledCert, error) {
	b.logger.Debug("Enroll called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Enroll failed", "error", err)
		return nil, err
	}
	enrolled, err := cl.Certs().EnrollCert(context.Background(), req)
	if err != nil {
		b.logger.Error("Enroll failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Enroll completed")
	return enrolled, nil
}

// ExportCertInput asks the desktop to build a PKCS#12 archive from a
// certificate and key already held on this machine. CAPEM is an optional
// issuer certificate to embed in the archive's chain.
type ExportCertInput struct {
	CertPEM    string `json:"certPEM"`
	KeyPEM     string `json:"keyPEM"`
	CAPEM      string `json:"caPEM,omitempty"`
	Passphrase string `json:"passphrase"`
}

// ExportCertOutput is the base64-encoded PKCS#12 archive.
type ExportCertOutput struct {
	PKCS12 string `json:"pkcs12"`
}

// Export builds a PKCS#12 archive ENTIRELY LOCALLY from a certificate and
// its private key, both already on this machine. There is no server call:
// no node may receive a private key to encode on a caller's behalf
// (go-qrdb security review), so the archive is built in this process, the
// same one that already holds the key.
func (b *CertBinding) Export(req *ExportCertInput) (*ExportCertOutput, error) {
	b.logger.Debug("Export called")
	if req == nil {
		err := &ExportCertNilInputError{}
		b.logger.Error("Export failed", "error", err)
		return nil, err
	}
	p12, err := sdk.EncodePKCS12([]byte(req.CertPEM), []byte(req.KeyPEM), []byte(req.CAPEM), req.Passphrase)
	if err != nil {
		b.logger.Error("Export failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Export completed")
	return &ExportCertOutput{PKCS12: base64.StdEncoding.EncodeToString(p12)}, nil
}

// Revoke marks a certificate serial as revoked and reports whether the
// server held a local record for it (deny-by-serial revocations succeed
// with no record found).
func (b *CertBinding) Revoke(serial string) (*transport.RevokeResult, error) {
	b.logger.Debug("Revoke called", "serial", serial)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Revoke failed", "error", err)
		return nil, err
	}
	result, err := cl.Certs().RevokeCert(context.Background(), serial)
	if err != nil {
		b.logger.Error("Revoke failed", "serial", serial, "error", err)
		return nil, err
	}
	b.logger.Debug("Revoke completed", "serial", serial, "recordFound", result.RecordFound)
	return result, nil
}

// List lists all issued client certificates for a tenant.
func (b *CertBinding) List(tenantID string) (*transport.ListCertsResponse, error) {
	b.logger.Debug("List called", "tenantID", tenantID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Certs().ListCerts(context.Background(), tenantID)
	if err != nil {
		b.logger.Error("List failed", "tenantID", tenantID, "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "tenantID", tenantID)
	return resp, nil
}
