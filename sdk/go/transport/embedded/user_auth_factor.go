// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time assertion: the embedded transport implements the M14
// user-side auth-factor surface. This is what makes
// (*sdk.Client).UserAuthFactors() return a non-nil client for the embedded
// path, so out-of-process composers (quicbaas) get the SAME Add / List /
// Get / Remove / Rotate / Relabel surface in-process that the network
// transports expose. All operations are backed by the SAME
// userAuthFactorSvc the unified-login ceremony resolves against, so factor
// ENROLLMENT writes are immediately visible to login READS.
var _ transport.UserAuthFactorClient = (*Transport)(nil)

// userAuthRecordToDomain projects the wire AuthFactorRecord onto the domain
// auth.AuthFactor used by the service layer.
func userAuthRecordToDomain(rec *transport.AuthFactorAddRequest, userID uint64) *auth.AuthFactor {
	out := &auth.AuthFactor{
		UserID:     userID,
		Type:       auth.AuthFactorType(rec.Type),
		Label:      rec.Label,
		Credential: append([]byte(nil), rec.Credential...),
		CreatedBy:  rec.CreatedBy,
	}
	if len(rec.SPKICredential) > 0 {
		out.SPKICredential = append([]byte(nil), rec.SPKICredential...)
	}
	if len(rec.Metadata) > 0 {
		out.Metadata = make(map[string]string, len(rec.Metadata))
		for k, v := range rec.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// domainToUserAuthRecord projects a domain auth.AuthFactor onto the wire
// AuthFactorRecord.
func domainToUserAuthRecord(f *auth.AuthFactor) transport.AuthFactorRecord {
	if f == nil {
		return transport.AuthFactorRecord{}
	}
	out := transport.AuthFactorRecord{
		ID:           f.ID,
		UserID:       f.UserID,
		Type:         string(f.Type),
		Label:        f.Label,
		Credential:   append([]byte(nil), f.Credential...),
		RegisteredAt: f.RegisteredAt,
		LastUsedAt:   f.LastUsedAt,
		RevokedAt:    f.RevokedAt,
		CreatedBy:    f.CreatedBy,
	}
	if len(f.SPKICredential) > 0 {
		out.SPKICredential = append([]byte(nil), f.SPKICredential...)
	}
	if len(f.Metadata) > 0 {
		out.Metadata = make(map[string]string, len(f.Metadata))
		for k, v := range f.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// AddAuthFactor persists a new factor for a user via the user-side service.
func (t *Transport) AddAuthFactor(ctx context.Context, req *transport.AuthFactorAddRequest) (*transport.AuthFactorAddResponse, error) {
	ctx, err := t.admit(ctx, "AddAuthFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "AddAuthFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if req == nil {
		return nil, &transport.FactorNotFoundError{}
	}
	domain := userAuthRecordToDomain(req, req.UserID)
	version, err := service.AddAuthFactor(ctx, t.userAuthFactorSvc, req.UserID, domain, req.Condition())
	if err != nil {
		return nil, mapFactorError(err)
	}
	return &transport.AuthFactorAddResponse{Factor: domainToUserAuthRecord(domain), FactorSetVersion: version}, nil
}

// ListAuthFactors returns every factor (active and revoked) for a user.
func (t *Transport) ListAuthFactors(ctx context.Context, req *transport.AuthFactorListRequest) (*transport.AuthFactorListResponse, error) {
	ctx, err := t.admit(ctx, "ListAuthFactors")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListAuthFactors"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if req == nil {
		return nil, &transport.FactorNotFoundError{}
	}
	factors, version, err := service.ListAuthFactors(ctx, t.userAuthFactorSvc, req.UserID)
	if err != nil {
		return nil, mapFactorError(err)
	}
	out := &transport.AuthFactorListResponse{
		Factors:          make([]transport.AuthFactorRecord, 0, len(factors)),
		Total:            len(factors),
		FactorSetVersion: version,
	}
	for _, f := range factors {
		out.Factors = append(out.Factors, domainToUserAuthRecord(f))
	}
	return out, nil
}

// GetAuthFactor returns the factor with the given ID.
func (t *Transport) GetAuthFactor(ctx context.Context, req *transport.AuthFactorGetRequest) (*transport.AuthFactorGetResponse, error) {
	ctx, err := t.admit(ctx, "GetAuthFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetAuthFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if req == nil || req.FactorID == "" {
		return nil, &transport.FactorNotFoundError{}
	}
	f, err := t.userAuthFactorSvc.Get(ctx, req.FactorID)
	if err != nil {
		return nil, mapFactorError(err)
	}
	return &transport.AuthFactorGetResponse{Factor: domainToUserAuthRecord(f)}, nil
}

// RemoveAuthFactor soft-deletes a factor (honouring the force flag).
func (t *Transport) RemoveAuthFactor(ctx context.Context, req *transport.AuthFactorRemoveRequest) (*transport.AuthFactorRemoveResponse, error) {
	ctx, err := t.admit(ctx, "RemoveAuthFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "RemoveAuthFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if req == nil || req.FactorID == "" {
		return nil, &transport.FactorNotFoundError{}
	}
	if err := t.userAuthFactorSvc.Remove(ctx, req.FactorID, req.Force); err != nil {
		return nil, mapFactorError(err)
	}
	return &transport.AuthFactorRemoveResponse{Status: "revoked"}, nil
}

// DeleteAuthFactor hard-deletes a factor together with its credential, SPKI
// and user index entries, so the credential can be bound again. It rolls
// back an add; a revoked factor is refused and stays revoked.
func (t *Transport) DeleteAuthFactor(ctx context.Context, req *transport.AuthFactorDeleteRequest) (*transport.AuthFactorDeleteResponse, error) {
	ctx, err := t.admit(ctx, "DeleteAuthFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DeleteAuthFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if req == nil || req.FactorID == "" {
		return nil, &transport.FactorNotFoundError{}
	}
	if err := t.userAuthFactorSvc.Delete(ctx, req.FactorID, req.Force); err != nil {
		return nil, mapFactorError(err)
	}
	return &transport.AuthFactorDeleteResponse{Status: "deleted"}, nil
}

// RotateAuthFactor atomically adds the new factor and revokes the old one.
func (t *Transport) RotateAuthFactor(ctx context.Context, req *transport.AuthFactorRotateRequest) (*transport.AuthFactorRotateResponse, error) {
	ctx, err := t.admit(ctx, "RotateAuthFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "RotateAuthFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if req == nil || req.OldFactorID == "" {
		return nil, &transport.FactorNotFoundError{FactorID: ""}
	}
	if err := req.Validate(); err != nil {
		return nil, mapFactorError(err)
	}
	newDomain := userAuthRecordToDomain(&req.NewFactor, req.NewFactor.UserID)
	if err := t.userAuthFactorSvc.Rotate(ctx, req.OldFactorID, newDomain); err != nil {
		return nil, mapFactorError(err)
	}
	return &transport.AuthFactorRotateResponse{NewFactor: domainToUserAuthRecord(newDomain)}, nil
}

// RelabelAuthFactor updates the label on a factor.
func (t *Transport) RelabelAuthFactor(ctx context.Context, req *transport.AuthFactorRelabelRequest) (*transport.AuthFactorRelabelResponse, error) {
	ctx, err := t.admit(ctx, "RelabelAuthFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "RelabelAuthFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if req == nil || req.FactorID == "" {
		return nil, &transport.FactorNotFoundError{}
	}
	if err := t.userAuthFactorSvc.Relabel(ctx, req.FactorID, req.Label); err != nil {
		return nil, mapFactorError(err)
	}
	f, err := t.userAuthFactorSvc.Get(ctx, req.FactorID)
	if err != nil {
		return nil, mapFactorError(err)
	}
	return &transport.AuthFactorRelabelResponse{Factor: domainToUserAuthRecord(f)}, nil
}

// LoginAuthFactor resolves an inbound (Type, LookupKey) to a principal and
// factor via the user-side store. Hardware verification is the caller's
// responsibility; this only performs the database mapping, mirroring the
// network transports' contract.
func (t *Transport) LoginAuthFactor(ctx context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	ctx, err := t.admit(ctx, "LoginAuthFactor")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LoginAuthFactor"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.loginResolver == nil {
		return nil, &transport.NotImplementedError{Method: "LoginAuthFactor"}
	}
	if req == nil {
		return nil, &transport.FactorNotFoundError{}
	}
	principal, factor, err := t.loginResolver.Resolve(ctx, auth.AuthFactorType(req.Type), req.LookupKey)
	if err != nil {
		return nil, mapFactorError(err)
	}
	resp := &transport.AuthFactorLoginResponse{
		UserID: principal.ID,
		Factor: domainToUserAuthRecord(factor),
	}
	resp.Username = principal.Username
	resp.TenantID = principal.TenantID
	resp.Roles = principal.Roles
	resp.Active = principal.Active
	return resp, nil
}

// UserAuthFactorClient returns the embedded user-side auth-factor client.
// The Transport itself satisfies transport.UserAuthFactorClient, so the SDK
// client's type assertion in (*Client).UserAuthFactors() succeeds for the
// embedded transport.
func (t *Transport) UserAuthFactorClient() transport.UserAuthFactorClient {
	return t
}
