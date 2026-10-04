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

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CeremonyHolderCertificate is one enrolled holder's CA-issued certificate,
// part of a successful CeremonyInitializeResult. Mirrors
// transport.CeremonyHolderCert with camelCase JSON tags, the desktop
// binding convention (BootstrapResult, SealStatus, ...).
type CeremonyHolderCertificate struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	CertPEM  string `json:"certPem"`
}

// CeremonyInitializeResult mirrors transport.CeremonyInitializeResponse for
// the desktop GUI, with one deliberate difference: AdminKeyPath replaces
// AdminKeyPEM. When the bootstrap admin's credential is generated, its
// private key is saved to a local file by THIS process -- the desktop's own
// machine -- and never crosses the Wails IPC boundary to the page at all;
// AdminKeyPath names where it was written. Unlike REST (where the
// generated key is returned to the browser because there is no "local
// machine" of its own to save it to), the desktop binding IS already
// running on the user's own machine, so saving it here is the local save,
// not an extra hop.
type CeremonyInitializeResult struct {
	SPKIPin               string                      `json:"spkiPin"`
	CACertPEM             string                      `json:"caCertPem"`
	TLSCertPEM            string                      `json:"tlsCertPem"`
	State                 string                      `json:"state"`
	ShareCount            int                         `json:"shareCount"`
	Holders               []CeremonyHolderCertificate `json:"holders,omitempty"`
	AdminCertPEM          string                      `json:"adminCertPem,omitempty"`
	AdminKeyPath          string                      `json:"adminKeyPath,omitempty"`
	AdminCredentialSource string                      `json:"adminCredentialSource,omitempty"`
}

// CeremonyBinding exposes the initialization ceremony -- `qrdb init`'s
// server-side counterpart -- to the desktop GUI by delegating to the SDK
// CeremonyClient on the active connection. Like OfficerBinding, it works
// equally against an embedded server or a remote daemon: it holds no
// ceremony state of its own, only the connection.
type CeremonyBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewCeremonyBinding creates a new CeremonyBinding backed by the given
// connection.
func NewCeremonyBinding(log *logger.Logger, conn *ConnectionBinding) *CeremonyBinding {
	return &CeremonyBinding{logger: log, conn: conn}
}

// Initialize runs the initialization ceremony: brings up the system
// barrier, resolves the server's TLS identity, and issues the bootstrap
// admin's certificate, all in one call. A Threshold of 2 or more
// additionally initializes Shamir custody over Officers.
//
// req belongs to the CALLER (see SealBinding.EnableEncryption): nothing
// here guarantees exclusive ownership of req.Secret once Wails has
// unmarshaled it, so it is cloned into this call's own copy before being
// wiped.
//
// resp.AdminKeyPEM, when present (the admin-generate path), is never
// returned to the page at all: it is saved to a local file by this
// process -- see saveAdminKeyLocally -- and wiped from memory immediately
// after, before this method returns. Every other field crosses the Wails
// IPC boundary unchanged; this is the one field that does not, by design
// (go-qrdb#560 review item 7's wipe-ordering reasoning does not apply
// here, because the key is never handed to Wails to marshal in the first
// place).
func (b *CeremonyBinding) Initialize(req *transport.CeremonyInitializeRequest) (*CeremonyInitializeResult, error) {
	b.logger.Debug("Initialize called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Initialize failed", "error", err)
		return nil, err
	}

	call := *req
	call.Secret = bytes.Clone(req.Secret)
	defer secure.Wipe(call.Secret)

	resp, err := cl.Ceremony().CeremonyInitialize(context.Background(), &call)
	if err != nil {
		b.logger.Error("Initialize failed", "error", err)
		return nil, err
	}
	defer secure.Wipe(resp.AdminKeyPEM)

	result := &CeremonyInitializeResult{
		SPKIPin:               resp.SPKIPin,
		CACertPEM:             resp.CACertPEM,
		TLSCertPEM:            resp.TLSCertPEM,
		State:                 resp.State,
		ShareCount:            resp.ShareCount,
		AdminCertPEM:          resp.AdminCertPEM,
		AdminCredentialSource: resp.AdminCredentialSource,
	}
	for _, h := range resp.Holders {
		result.Holders = append(result.Holders, CeremonyHolderCertificate{Username: h.Username, Role: h.Role, CertPEM: h.CertPEM})
	}
	if len(resp.AdminKeyPEM) > 0 {
		path, serr := saveAdminKeyLocally(resp.AdminKeyPEM)
		if serr != nil {
			b.logger.Error("Initialize: failed to save the generated admin key locally", "error", serr)
			return nil, serr
		}
		result.AdminKeyPath = path
		b.logger.Info("Initialize: generated admin key saved locally", "path", path)
	}
	b.logger.Debug("Initialize completed", "state", result.State)
	return result, nil
}

// saveAdminKeyLocally writes a generated bootstrap admin private key to a
// fresh file under this machine's qrdb config directory (the same
// directory EmbeddedServerBinding.resolveDataDir's sibling "data" lives
// under), mode 0600, and returns its path. The key is NEVER returned to
// the page: this is the "never through the page" save the dev/test/
// evaluation-only --admin-generate path requires, mirroring how
// cmd/qrdb/init.go prints the same key to the operator's own terminal --
// a channel the page is not.
func saveAdminKeyLocally(keyPEM []byte) (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", &DataDirCreateError{Path: "~/.config", Err: err}
	}
	dir := filepath.Join(configDir, "qrdb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", &DataDirCreateError{Path: dir, Err: err}
	}
	path := filepath.Join(dir, fmt.Sprintf("admin-key-%d.pem", time.Now().UnixNano()))
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		return "", &FileWriteError{Path: path, Err: err}
	}
	return path, nil
}

// Status returns the current ceremony state.
func (b *CeremonyBinding) Status() (*transport.CeremonyStatusResponse, error) {
	b.logger.Debug("Status called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}
	resp, err := cl.Ceremony().CeremonyStatus(context.Background())
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Status completed", "state", resp.State)
	return resp, nil
}

// SignCSR submits a CSR for signing during or after the ceremony, issuing
// a holder certificate under SO PIN authorization.
func (b *CeremonyBinding) SignCSR(req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	b.logger.Debug("SignCSR called", "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("SignCSR failed", "error", err)
		return nil, err
	}
	resp, err := cl.Ceremony().CeremonySignCSR(context.Background(), req)
	if err != nil {
		b.logger.Error("SignCSR failed", "error", err)
		return nil, err
	}
	b.logger.Debug("SignCSR completed")
	return resp, nil
}
