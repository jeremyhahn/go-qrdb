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

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

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
// resp.AdminKeyPEM, when present, is NOT wiped here. It is the one copy
// the caller receives, and this method's "write" of it is Wails' own JSON
// marshal of the return value, which happens after this method returns --
// there is no later hook in this process to wipe after that write
// completes, unlike REST/gRPC/QUIC/MCP, which write the response
// synchronously inside the handler body before any deferred wipe fires
// (go-qrdb#560 review item 7 applies the same reasoning at every transport
// boundary; this boundary's answer is "don't wipe here," matching how
// cmd/qrdb/init.go leaves the CLI's own copy to its own deferred wipe only
// after printing it).
func (b *CeremonyBinding) Initialize(req *transport.CeremonyInitializeRequest) (*transport.CeremonyInitializeResponse, error) {
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
	b.logger.Debug("Initialize completed", "state", resp.State)
	return resp, nil
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
