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
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// HolderScope selects the barrier a desktop holder operation acts on: the
// system barrier in Domain ("unseal" when empty), the tenant barrier of
// TenantID, or the barrier of Database in the connection's tenant. At most
// one of TenantID and Database is set.
type HolderScope struct {
	TenantID string `json:"tenant_id,omitempty"`
	Database string `json:"database,omitempty"`
	Domain   string `json:"domain,omitempty"`
}

// kind returns the barrier kind scope names and the value that selects it.
func (s HolderScope) kind() (holderKind, string, error) {
	switch {
	case s.TenantID != "" && s.Database != "":
		return 0, "", &InvalidArgError{Arg: "scope", Reason: "names both a tenant and a database"}
	case s.Database != "":
		return holderDatabase, s.Database, nil
	case s.TenantID != "":
		return holderTenant, s.TenantID, nil
	case s.Domain == "":
		return holderSystem, transport.DomainUnseal, nil
	default:
		return holderSystem, s.Domain, nil
	}
}

// HolderClaimParams claims a holder's sealed envelope in the desktop app.
// KeyPEM is the holder's certificate private key; it signs the claim nonce
// in this process and is never sent anywhere.
type HolderClaimParams struct {
	HolderScope
	Username string `json:"username"`
	KeyPEM   string `json:"key_pem"`
}

// HolderSubmitParams submits a holder's share to the open quorum round.
// Envelope is the claimed envelope; KeyPEM is the certificate key that
// answers the mtls factor and, without EncryptionKey, opens the envelope.
// Keys stay in this process: only the factor response and the share leave.
type HolderSubmitParams struct {
	HolderScope
	Op            string `json:"op"`
	Username      string `json:"username"`
	Envelope      string `json:"envelope"`
	KeyPEM        string `json:"key_pem"`
	EncryptionKey []byte `json:"encryption_key,omitempty"`
}

// HolderRekeyParams opens a rekey round, or a revocation when Username is
// set.
type HolderRekeyParams struct {
	HolderScope
	Username  string `json:"username,omitempty"`
	Threshold int    `json:"threshold"`
	Policy    string `json:"policy,omitempty"`
	SOPin     string `json:"so_pin,omitempty"`
}

// OfficerBinding exposes share-holder enrolment, the envelope claim, and
// quorum submission to the Wails frontend for the system barrier, tenant
// barriers and database barriers. It drives the same holderTarget the CLI
// does.
type OfficerBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewOfficerBinding creates a new OfficerBinding backed by the given
// connection.
func NewOfficerBinding(log *logger.Logger, conn *ConnectionBinding) *OfficerBinding {
	return &OfficerBinding{logger: log, conn: conn}
}

// target resolves the holderTarget for scope over the connected client.
func (b *OfficerBinding) target(scope HolderScope) (holderTarget, error) {
	cl, err := b.conn.loadClient()
	if err != nil {
		return nil, err
	}
	kind, value, err := scope.kind()
	if err != nil {
		return nil, err
	}
	return holderTargets[kind](cl, value)
}

// logged runs op, logging its outcome under name.
func logged[T any](b *OfficerBinding, name string, op func() (T, error)) (T, error) {
	b.logger.Debug(name + " called")
	out, err := op()
	if err != nil {
		b.logger.Error(name+" failed", "error", err)
		return out, err
	}
	b.logger.Debug(name + " completed")
	return out, nil
}

// RegisterHolder enrols a share holder (Security Officer or key custodian).
func (b *OfficerBinding) RegisterHolder(scope HolderScope, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	return logged(b, "RegisterHolder", func() (*transport.OfficerRegisterResponse, error) {
		target, err := b.target(scope)
		if err != nil {
			return nil, err
		}
		return target.register(context.Background(), req)
	})
}

// ListHolders lists the holders of scope and their claim state.
func (b *OfficerBinding) ListHolders(scope HolderScope) ([]*transport.OfficerStatus, error) {
	return logged(b, "ListHolders", func() ([]*transport.OfficerStatus, error) {
		target, err := b.target(scope)
		if err != nil {
			return nil, err
		}
		return target.list(context.Background())
	})
}

// ClaimEnvelope runs the claim handshake, signing the nonce with the
// holder's certificate key, and returns the sealed envelope. The connection
// must present the holder's certificate.
func (b *OfficerBinding) ClaimEnvelope(req *HolderClaimParams) (*transport.ShareClaimCompleteResponse, error) {
	return logged(b, "ClaimEnvelope", func() (*transport.ShareClaimCompleteResponse, error) {
		target, err := b.target(req.HolderScope)
		if err != nil {
			return nil, err
		}
		keyPEM := []byte(req.KeyPEM)
		defer secure.Wipe(keyPEM)
		signer, err := mtls.ParseSignerPEM(keyPEM)
		if err != nil {
			return nil, err
		}
		ctx := context.Background()
		begin, err := target.claimBegin(ctx, req.Username)
		if err != nil {
			return nil, err
		}
		sig, err := signClaim(signer, target, req.Username, begin.NonceHex)
		if err != nil {
			return nil, err
		}
		return target.claimComplete(ctx, req.Username, begin.NonceHex, sig)
	})
}

// SubmitShare answers the mtls factor, opens the holder's envelope and
// submits its share to the open round for the operation.
func (b *OfficerBinding) SubmitShare(req *HolderSubmitParams) (*transport.QuorumOutcome, error) {
	return logged(b, "SubmitShare", func() (*transport.QuorumOutcome, error) {
		target, err := b.target(req.HolderScope)
		if err != nil {
			return nil, err
		}
		keyPEM := []byte(req.KeyPEM)
		defer secure.Wipe(keyPEM)
		defer secure.Wipe(req.EncryptionKey)
		signer, err := mtls.ParseSignerPEM(keyPEM)
		if err != nil {
			return nil, err
		}
		return submitHolderShare(context.Background(), target, &holderSubmission{
			Op:       req.Op,
			Username: req.Username,
			Envelope: []byte(req.Envelope),
			CertKey:  signer,
			EncKey:   req.EncryptionKey,
		})
	})
}

// StartRekey opens a quorum-gated rekey round.
func (b *OfficerBinding) StartRekey(req *HolderRekeyParams) (*transport.QuorumRound, error) {
	return logged(b, "StartRekey", func() (*transport.QuorumRound, error) {
		target, err := b.target(req.HolderScope)
		if err != nil {
			return nil, err
		}
		return target.rekey(context.Background(), req.Threshold, req.Policy, req.SOPin)
	})
}

// InitializeDatabaseShamir splits a Shamir database's root key to its
// enrolled holders, at the M-of-N the database was created with.
func (b *OfficerBinding) InitializeDatabaseShamir(database, policy string) (*transport.BarrierInitializeShamirResponse, error) {
	return logged(b, "InitializeDatabaseShamir", func() (*transport.BarrierInitializeShamirResponse, error) {
		cl, err := b.conn.loadClient()
		if err != nil {
			return nil, err
		}
		return databaseHolders{cl: cl, database: database}.initialize(context.Background(), policy)
	})
}

// RevokeHolder revokes a holder through a quorum-gated rekey.
func (b *OfficerBinding) RevokeHolder(req *HolderRekeyParams) (*transport.QuorumRound, error) {
	return logged(b, "RevokeHolder", func() (*transport.QuorumRound, error) {
		target, err := b.target(req.HolderScope)
		if err != nil {
			return nil, err
		}
		return target.revoke(context.Background(), req.Username, req.Threshold, req.SOPin)
	})
}

// UnenrolDatabaseHolder removes a holder of a Shamir database before its
// custody is initialized; after it a holder is removed with RevokeHolder.
func (b *OfficerBinding) UnenrolDatabaseHolder(database, username string) error {
	_, err := logged(b, "UnenrolDatabaseHolder", func() (struct{}, error) {
		cl, err := b.conn.loadClient()
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, databaseHolders{cl: cl, database: database}.unenrol(context.Background(), username)
	})
	return err
}
