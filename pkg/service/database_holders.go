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

package service

import (
	"context"
	"encoding/hex"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// DatabaseHolderServicer is the share-holder custody of a database barrier
// under the shamir strategy: the same operations a tenant barrier's holders
// have, scoped to one database in the caller's tenant (docs/databases). A
// database barrier has the unseal domain only.
//
// It is additive to DatabaseServicer, as DatabaseCredentialCreator is:
// transports type-assert the resolved DatabaseServicer for it, and
// *DatabaseService implements it.
type DatabaseHolderServicer interface {
	// DatabaseBarrierOfficerRegister enrols a share holder of the database's
	// barrier. The authenticated caller is the enroller and may not enrol
	// itself.
	DatabaseBarrierOfficerRegister(ctx context.Context, name string, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error)
	// DatabaseBarrierOfficerList returns every holder with its claim state.
	DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*BarrierOfficerStatus, error)
	// DatabaseBarrierShamirInitialize splits the database's new root key to
	// its enrolled holders, at the M-of-N the database was created with.
	DatabaseBarrierShamirInitialize(ctx context.Context, name string, policy seal.FactorPolicy) (*ShamirInitInfo, error)
	// DatabaseBarrierShareClaimBegin issues a holder's claim nonce.
	DatabaseBarrierShareClaimBegin(ctx context.Context, name, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error)
	// DatabaseBarrierShareClaimComplete delivers a holder's envelope once.
	DatabaseBarrierShareClaimComplete(ctx context.Context, name, username, nonceHex string, signature []byte, callerCertThumbprint string) (*BarrierShareClaimResult, error)
	// DatabaseBarrierQuorumBegin issues a holder's quorum challenge.
	DatabaseBarrierQuorumBegin(ctx context.Context, name string, req *QuorumBeginRequest) (*QuorumChallengeInfo, error)
	// DatabaseBarrierQuorumSubmit counts one authenticated holder's share.
	DatabaseBarrierQuorumSubmit(ctx context.Context, name string, req *QuorumSubmitRequest) (*QuorumOutcomeInfo, error)
	// DatabaseBarrierRekeyStart opens a rekey round.
	DatabaseBarrierRekeyStart(ctx context.Context, name string, threshold int, policy seal.FactorPolicy) (*QuorumRoundInfo, error)
	// DatabaseBarrierOfficerRevoke opens a rekey round that removes a holder.
	DatabaseBarrierOfficerRevoke(ctx context.Context, name, username string, threshold int) (*QuorumRoundInfo, error)
}

// Compile-time interface check.
var _ DatabaseHolderServicer = (*DatabaseService)(nil)

// databaseHolders resolves the holder authority and barrier of the named
// database's Shamir barrier after the ownership check every holder operation
// shares: a non-owning tenant is refused before it learns whether the
// database or its barrier exists.
func (s *DatabaseService) databaseHolders(ctx context.Context, name string) (*ceremony.ScopedCeremonyService, *seal.StorageBarrier, error) {
	if err := s.authorizeDatabaseTenant(ctx, name); err != nil {
		return nil, nil, err
	}
	if s.barrierMgr == nil {
		return nil, nil, &BarrierManagerNotConfiguredError{}
	}
	return s.barrierMgr.Holders(name)
}

// DatabaseBarrierOfficerRegister implements DatabaseHolderServicer.
func (s *DatabaseService) DatabaseBarrierOfficerRegister(ctx context.Context, name string, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error) {
	holders, _, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNilRequest
	}
	result, err := holders.RegisterOfficer(ctx, HolderRegistration(ctx, req))
	if err != nil {
		return nil, err
	}
	s.holderLog("database share holder registered", name, slog.String("username", req.Username))
	return &BarrierOfficerRegisterResult{
		CertPEM:       result.CertPEM,
		Role:          result.Role,
		Designated:    result.Designated,
		EncryptionKEM: result.EncryptionKEM,
		Factors:       result.Factors,
	}, nil
}

// DatabaseBarrierOfficerList implements DatabaseHolderServicer.
func (s *DatabaseService) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*BarrierOfficerStatus, error) {
	holders, _, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	return holders.ListOfficers(ctx)
}

// DatabaseBarrierShamirInitialize implements DatabaseHolderServicer.
func (s *DatabaseService) DatabaseBarrierShamirInitialize(ctx context.Context, name string, policy seal.FactorPolicy) (*ShamirInitInfo, error) {
	if _, _, err := s.databaseHolders(ctx, name); err != nil {
		return nil, err
	}
	params, err := s.barrierMgr.InitializeShamir(ctx, name, policy)
	if err != nil {
		return nil, err
	}
	// Initialization leaves the barrier unsealed: the database is usable.
	s.setDatabaseStatus(name, database.StatusOpen)
	return &ShamirInitInfo{Threshold: params.Threshold, TotalShares: params.TotalShares, ShareCount: params.ShareCount}, nil
}

// DatabaseBarrierShareClaimBegin implements DatabaseHolderServicer.
func (s *DatabaseService) DatabaseBarrierShareClaimBegin(ctx context.Context, name, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error) {
	holders, _, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	challenge, err := holders.BeginClaimShare(ctx, username, callerCertThumbprint)
	if err != nil {
		return nil, err
	}
	return &BarrierShareClaimChallenge{NonceHex: hex.EncodeToString(challenge.Nonce), ExpiresAt: challenge.ExpiresAt}, nil
}

// DatabaseBarrierShareClaimComplete implements DatabaseHolderServicer.
func (s *DatabaseService) DatabaseBarrierShareClaimComplete(ctx context.Context, name, username, nonceHex string, signature []byte, callerCertThumbprint string) (*BarrierShareClaimResult, error) {
	holders, _, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	result, err := holders.CompleteClaimShare(ctx, username, nonceHex, signature, callerCertThumbprint)
	if err != nil {
		return nil, err
	}
	s.holderLog("database share envelope claimed", name, slog.String("username", username))
	return &BarrierShareClaimResult{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
}

// DatabaseBarrierQuorumBegin implements DatabaseHolderServicer. A database
// barrier has the unseal domain only, whatever the request names.
func (s *DatabaseService) DatabaseBarrierQuorumBegin(ctx context.Context, name string, req *QuorumBeginRequest) (*QuorumChallengeInfo, error) {
	holders, barrier, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrNilRequest
	}
	return holders.BeginQuorum(ctx, barrier, req.Op, req.Username, req.Factor)
}

// DatabaseBarrierQuorumSubmit implements DatabaseHolderServicer. A completed
// seal or unseal round is reflected on the database's status, and a
// completed rekey records the M-of-N it left, so the database reopens with
// the scheme its holders hold.
func (s *DatabaseService) DatabaseBarrierQuorumSubmit(ctx context.Context, name string, req *QuorumSubmitRequest) (*QuorumOutcomeInfo, error) {
	if req != nil {
		defer secure.Wipe(req.Share)
	}
	_, barrier, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	if req == nil || len(req.Share) == 0 {
		return nil, ErrNilRequest
	}
	out, err := barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: seal.DomainUnseal,
		Op:     req.Op,
		Holder: req.Username,
		Proof: seal.HolderProof{
			ChallengeID:        req.ChallengeID,
			Factor:             req.Factor,
			Response:           req.Response,
			ResponseMetadata:   req.ResponseMetadata,
			PeerCertThumbprint: req.PeerCertThumbprint,
		},
		Share: req.Share,
	})
	if err != nil {
		return nil, err
	}
	if out.Complete {
		switch out.Op {
		case seal.OpUnseal:
			s.setDatabaseStatus(name, database.StatusOpen)
		case seal.OpSeal:
			s.setDatabaseStatus(name, database.StatusSealed)
		case seal.OpRekey:
			if out.Rekey != nil {
				if rerr := s.manager.RecordShamirScheme(name, out.Rekey.Threshold, out.Rekey.TotalShares); rerr != nil {
					return nil, rerr
				}
			}
		}
		s.holderLog("database barrier holder quorum complete", name, slog.String("op", string(out.Op)))
	}
	return QuorumOutcomeFrom(out), nil
}

// DatabaseBarrierRekeyStart implements DatabaseHolderServicer.
func (s *DatabaseService) DatabaseBarrierRekeyStart(ctx context.Context, name string, threshold int, policy seal.FactorPolicy) (*QuorumRoundInfo, error) {
	_, barrier, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	return barrier.StartRekey(ctx, seal.DomainUnseal, seal.RekeyParams{Threshold: threshold, Policy: policy})
}

// DatabaseBarrierOfficerRevoke implements DatabaseHolderServicer: a rekey
// round that removes the holder, refused when the remaining holders cannot
// meet the threshold.
func (s *DatabaseService) DatabaseBarrierOfficerRevoke(ctx context.Context, name, username string, threshold int) (*QuorumRoundInfo, error) {
	if username == "" {
		return nil, ceremony.ErrMissingUsername
	}
	_, barrier, err := s.databaseHolders(ctx, name)
	if err != nil {
		return nil, err
	}
	return barrier.StartRekey(ctx, seal.DomainUnseal, seal.RekeyParams{Threshold: threshold, Remove: []string{username}})
}

// setDatabaseStatus reflects a barrier transition on the database's status.
func (s *DatabaseService) setDatabaseStatus(name string, status database.StatusType) {
	if db, err := s.manager.Get(name); err == nil {
		db.SetStatus(status)
	}
}

// holderLog logs a database holder event when a logger is configured.
func (s *DatabaseService) holderLog(msg, name string, attrs ...slog.Attr) {
	if s.logger == nil {
		return
	}
	args := []any{slog.String("database", name)}
	for _, a := range attrs {
		args = append(args, a)
	}
	s.logger.Info(msg, args...)
}
