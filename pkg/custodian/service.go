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

package custodian

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

const (
	// sysCustodianPrefix is the primary KV namespace for custodian records.
	sysCustodianPrefix = "__sys:custodian/"

	// sysCustodianIdxTenant is the tenant secondary index prefix.
	// Key: __sys:custodian-idx/tenant/{tenantID}/{custodianID}.
	sysCustodianIdxTenant = "__sys:custodian-idx/tenant/"

	// sysCustodianIdxUsername is the username secondary index prefix.
	// Key: __sys:custodian-idx/username/{tenantID}/{username}.
	sysCustodianIdxUsername = "__sys:custodian-idx/username/"
)

// Servicer manages the lifecycle of custodians: registration, retrieval,
// listing, and revocation. Share material is never stored; only the public
// metadata record is persisted.
type Servicer interface {
	// RegisterCustodian creates a custodian metadata record, seals the
	// provided Shamir share into an ECDH envelope, and returns the
	// RegisterResponse. The share plaintext is zeroed after sealing.
	// Returns ErrCustodianAlreadyExists if the username already exists
	// for the tenant.
	RegisterCustodian(ctx context.Context, req RegisterRequest) (*RegisterResponse, error)

	// GetCustodian retrieves a custodian by its unique ID.
	// Returns ErrCustodianNotFound when absent.
	GetCustodian(ctx context.Context, id string) (*Custodian, error)

	// ListCustodians returns all custodians for the given tenant.
	ListCustodians(ctx context.Context, tenantID string) ([]*Custodian, error)

	// RevokeCustodian marks the custodian as revoked. Idempotent: revoking
	// an already-revoked custodian returns nil.
	RevokeCustodian(ctx context.Context, id string) error
}

// service is the default Servicer implementation backed by a kvstore.KVStore.
type service struct {
	store  kvstore.KVStore
	audit  audit.AuditLogger
	logger *slog.Logger
}

// Compile-time interface check.
var _ Servicer = (*service)(nil)

// NewService constructs a Servicer backed by the provided KV store.
// Both store and logger must be non-nil.
func NewService(store kvstore.KVStore, auditLogger audit.AuditLogger, logger *slog.Logger) (Servicer, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if auditLogger == nil {
		return nil, ErrNilAuditLogger
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	return &service{store: store, audit: auditLogger, logger: logger}, nil
}

// RegisterCustodian validates the request, derives the custodian's
// deterministic ID from tenantID+username, seals the Shamir share into an
// ECDH envelope, persists the custodian metadata, and returns the sealed
// envelope. The share plaintext bytes are zeroed by SealShareTo before this
// method returns.
func (s *service) RegisterCustodian(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	if err := validateRegisterRequest(req); err != nil {
		s.audit.Emit(ctx, audit.AuditEvent{
			Resource:   "custodian",
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":" + err.Error(),
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID},
		})
		return nil, err
	}

	// Duplicate registration is decided from the username index, not by
	// looking up the derived ID. The index is the record of what this tenant
	// has actually enrolled, so it catches a custodian registered before
	// custodian IDs moved off UUIDv5/SHA-1 (see custodianID), whose record
	// still lives under its legacy identifier. Re-deriving instead would
	// miss it and enrol the same person twice, under two IDs, each holding a
	// share.
	existing, err := s.store.Get(ctx, custodianUsernameKey(req.TenantID, req.Username))
	if err != nil {
		return nil, &StoreError{Op: "check-exists", Err: err}
	}
	if existing != nil {
		// The index value is the enrolled custodian's own ID, so the audit
		// event names the record that actually exists rather than the one
		// this call would have created.
		s.audit.Emit(ctx, audit.AuditEvent{
			Resource:   "custodian:" + string(existing),
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":already-exists",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID},
		})
		return nil, ErrCustodianAlreadyExists
	}

	id := custodianID(req.TenantID, req.Username)

	// Seal the share into an ECDH envelope. SealShareTo zeroes req.ShareBytes.
	envelope, err := SealShareTo(req.ShareBytes, req.PublicKeyPEM)
	if err != nil {
		s.audit.Emit(ctx, audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":envelope-seal",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID},
		})
		return nil, err
	}

	now := time.Now().UTC()
	c := &Custodian{
		ID:           id,
		Username:     req.Username,
		TenantID:     req.TenantID,
		Method:       req.Method,
		EnrollmentID: req.EnrollmentID,
		PublicKeyPEM: req.PublicKeyPEM,
		ShareIndex:   req.ShareIndex,
		EnrolledAt:   now,
	}

	if err := s.persist(ctx, c); err != nil {
		return nil, err
	}

	s.logger.Info("custodian registered",
		slog.String("id", id),
		slog.String("username", req.Username),
		slog.String("tenantID", req.TenantID),
		slog.Int("shareIndex", req.ShareIndex))

	s.audit.Emit(ctx, audit.AuditEvent{
		Resource: "custodian:" + id,
		Action:   "register",
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"username":   req.Username,
			"tenantID":   req.TenantID,
			"method":     string(req.Method),
			"shareIndex": req.ShareIndex,
		},
	})

	return &RegisterResponse{
		Custodian:      c,
		SealedEnvelope: envelope,
	}, nil
}

// GetCustodian retrieves a custodian by its unique ID.
func (s *service) GetCustodian(ctx context.Context, id string) (*Custodian, error) {
	data, err := s.store.Get(ctx, sysCustodianPrefix+id)
	if err != nil {
		return nil, &StoreError{Op: "get", Err: err}
	}
	if data == nil {
		return nil, ErrCustodianNotFound
	}
	var c Custodian
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, &StoreError{Op: "unmarshal", Err: err}
	}
	return &c, nil
}

// ListCustodians returns all custodian records for the given tenant by
// scanning the tenant secondary index.
func (s *service) ListCustodians(ctx context.Context, tenantID string) ([]*Custodian, error) {
	prefix := sysCustodianIdxTenant + tenantID + "/"
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return nil, &StoreError{Op: "list", Err: err}
	}

	custodians := make([]*Custodian, 0, len(keys))
	for _, key := range keys {
		// Index keys end with the custodian ID.
		custodianID := key[len(prefix):]
		c, err := s.GetCustodian(ctx, custodianID)
		if err != nil {
			s.logger.Warn("custodian list: skipping missing record",
				slog.String("key", key),
				slog.String("error", err.Error()))
			continue
		}
		custodians = append(custodians, c)
	}
	return custodians, nil
}

// RevokeCustodian marks the custodian as revoked. Idempotent.
func (s *service) RevokeCustodian(ctx context.Context, id string) error {
	c, err := s.GetCustodian(ctx, id)
	if err != nil {
		s.audit.Emit(ctx, audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "revoke",
			Outcome:    audit.OutcomeFailure + ":not-found",
			Attributes: map[string]any{"id": id},
		})
		return err
	}

	// Idempotent: already revoked is success.
	if c.RevokedAt != nil {
		return nil
	}

	now := time.Now().UTC()
	c.RevokedAt = &now

	if err := s.persist(ctx, c); err != nil {
		s.audit.Emit(ctx, audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "revoke",
			Outcome:    audit.OutcomeFailure + ":store",
			Attributes: map[string]any{"id": id, "username": c.Username},
		})
		return err
	}

	s.logger.Info("custodian revoked",
		slog.String("id", id),
		slog.String("username", c.Username))

	s.audit.Emit(ctx, audit.AuditEvent{
		Resource: "custodian:" + id,
		Action:   "revoke",
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"id":       id,
			"username": c.Username,
			"tenantID": c.TenantID,
		},
	})
	return nil
}

// persist serialises the custodian record and writes it together with its
// secondary indexes to the KV store.
func (s *service) persist(ctx context.Context, c *Custodian) error {
	data, err := json.Marshal(c)
	if err != nil {
		return &StoreError{Op: "marshal", Err: err}
	}

	// Primary record.
	if err := s.store.Put(ctx, sysCustodianPrefix+c.ID, data); err != nil {
		return &StoreError{Op: "put", Err: err}
	}

	// Tenant secondary index.
	idxTenant := sysCustodianIdxTenant + c.TenantID + "/" + c.ID
	if err := s.store.Put(ctx, idxTenant, []byte(c.ID)); err != nil {
		return &StoreError{Op: "put-idx-tenant", Err: err}
	}

	// Username secondary index.
	if err := s.store.Put(ctx, custodianUsernameKey(c.TenantID, c.Username), []byte(c.ID)); err != nil {
		return &StoreError{Op: "put-idx-username", Err: err}
	}

	return nil
}

// validateRegisterRequest checks all required fields in a RegisterRequest.
func validateRegisterRequest(req RegisterRequest) error {
	if req.Username == "" {
		return ErrMissingUsername
	}
	if req.TenantID == "" {
		return ErrMissingTenantID
	}
	if req.EnrollmentID == "" {
		return ErrMissingEnrollmentID
	}
	if req.PublicKeyPEM == "" {
		return ErrMissingPublicKeyPEM
	}
	if req.ShareIndex < 1 {
		return ErrInvalidShareIndex
	}
	if len(req.ShareBytes) == 0 {
		return ErrEmptyShareBytes
	}
	switch req.Method {
	case FactorMethodPKCS11, FactorMethodPIV, FactorMethodFIDO2, FactorMethodSoftware, FactorMethodMTLS:
	default:
		return ErrInvalidMethod
	}
	return nil
}

// custodianUsernameKey builds the username secondary index key, the
// authoritative "has this tenant already enrolled this person" record.
func custodianUsernameKey(tenantID, username string) string {
	return sysCustodianIdxUsername + tenantID + "/" + username
}

// custodianNamespace domain-separates custodian identifiers from every other
// SHA-256 derivation in the system. UUIDv8 leaves the derivation entirely to
// the application (RFC 9562 §5.8), so unlike a v3/v5 namespace this is
// hashed as a literal prefix rather than as sixteen namespace bytes.
const custodianNamespace = "go-qrdb:custodian-namespace/v8/"

// custodianID derives a custodian's stable identifier from its tenant and
// username as an RFC 9562 §5.8 UUIDv8 over SHA-256.
//
// It replaces a UUIDv5 (name-based, SHA-1) derivation: SHA-1 does not exist
// under FIPS 140-3, so that call panicked outright in a binary built with
// GODEBUG=fips140=only. RFC 4122 fixes v3 to MD5 and v5 to SHA-1, so v8 —
// the version reserved for application-defined derivations — is the only
// name-based form an approved hash can take. The 36-character UUID text
// shape is unchanged, so no caller, key, or wire format reformats.
//
// The value differs from what the SHA-1 derivation produced for the same
// inputs. Custodians enrolled earlier keep resolving because every read
// addresses them by their stored identifier: GetCustodian and
// RevokeCustodian take the ID as given, ListCustodians reads it out of the
// tenant index, and RegisterCustodian's duplicate check goes through the
// username index rather than re-deriving.
//
// tenantID and username are length-prefixed rather than merely joined so
// that the encoding is injective: without framing, tenant "a:b" with user
// "c" and tenant "a" with user "b:c" would hash identically and two
// distinct custodians would collide on one identifier.
func custodianID(tenantID, username string) string {
	buf := make([]byte, 0, len(custodianNamespace)+len(tenantID)+len(username)+16)
	buf = append(buf, custodianNamespace...)
	buf = strconv.AppendInt(buf, int64(len(tenantID)), 10)
	buf = append(buf, ':')
	buf = append(buf, tenantID...)
	buf = strconv.AppendInt(buf, int64(len(username)), 10)
	buf = append(buf, ':')
	buf = append(buf, username...)

	sum := sha256.Sum256(buf)

	var u uuid.UUID
	copy(u[:], sum[:len(u)])
	u[6] = (u[6] & 0x0f) | 0x80 // version 8
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant (10xx)
	return u.String()
}
