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

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CertBinding exposes certificate management operations to the Wails frontend.
type CertBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewCertBinding creates a new CertBinding backed by the given connection.
func NewCertBinding(log *logger.Logger, conn *ConnectionBinding) *CertBinding {
	return &CertBinding{logger: log, conn: conn}
}

// Generate creates a client certificate signed by the internal CA.
func (b *CertBinding) Generate(req *transport.GenerateCertRequest) (*transport.CertBundle, error) {
	b.logger.Debug("Generate called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Generate failed", "error", err)
		return nil, err
	}
	bundle, err := cl.Certs().GenerateCert(context.Background(), req)
	if err != nil {
		b.logger.Error("Generate failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Generate completed")
	return bundle, nil
}

// Export exports a certificate and key as a PKCS#12 archive for browser import.
func (b *CertBinding) Export(req *transport.ExportCertRequest) (*transport.ExportCertResponse, error) {
	b.logger.Debug("Export called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Export failed", "error", err)
		return nil, err
	}
	resp, err := cl.Certs().ExportCert(context.Background(), req)
	if err != nil {
		b.logger.Error("Export failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Export completed")
	return resp, nil
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
