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

package firstboot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// PendingSystemSOKey is the record carrying the system SO the ceremony
// issued (ceremony.CeremonyService.LastIssuedSystemSO) to first boot, in
// the same replicated store and namespace as PendingAdminCertKey. The
// ceremony may run on a node that does not run first boot, so the SO
// crosses nodes here: its username, its certificate and chain, and its
// PIN as the auth.HashSOPIN record -- never the PIN.
//
// Composers write it BEFORE the pending admin certificate: both records
// live on one Raft shard, so a replica that sees the admin's record sees
// the SO's too, and first boot's wait for the admin covers the SO.
const PendingSystemSOKey = "pending-system-so"

// pendingSystemSO is the record's JSON form.
type pendingSystemSO struct {
	Username  string `json:"username"`
	CertPEM   []byte `json:"cert_pem"`
	ChainPEM  []byte `json:"chain_pem,omitempty"`
	PoolPEM   []byte `json:"pool_pem,omitempty"`
	PINRecord string `json:"pin_record"`
}

// WriteIssuedSystemSOIfAny records the system SO this node's ceremony
// issued, if it issued one. Like WriteIssuedAdminCertIfAny it is
// create-if-absent (the first ceremony's SO wins; a later one is not an
// error), proposes nothing when the record is already there, and waits
// out a leadership change for as long as transient says the error is one.
func WriteIssuedSystemSOIfAny(ctx context.Context, core *ceremony.CeremonyService, store PendingAdminCertStore, transient func(error) bool) error {
	if core == nil || store == nil {
		return nil
	}
	so, err := core.PendingSystemSO(ctx)
	if err != nil || so == nil {
		return err
	}
	recorded, err := readPendingSystemSO(ctx, store)
	if err != nil {
		return err
	}
	if bytes.Equal(recorded, pendingAdminCertConsumedMarker) {
		// First boot consumed it on an earlier start: the barrier's copy
		// has served its purpose.
		return core.ForgetPendingSystemSO(ctx)
	}
	if len(recorded) > 0 {
		return nil
	}
	record, err := json.Marshal(pendingSystemSO{
		Username: so.Username, CertPEM: so.CertPEM, ChainPEM: so.ChainPEM, PoolPEM: so.PoolPEM, PINRecord: so.PINRecord,
	})
	if err != nil {
		return &PendingAdminCertStoreError{Op: "encode system SO", Cause: err}
	}
	return writeSettled(ctx, transient, func() error {
		if _, err := store.PutIfAbsent(ctx, PendingSystemSOKey, record); err != nil {
			return &PendingAdminCertStoreError{Op: "write system SO", Cause: err}
		}
		return nil
	})
}

// PendingSystemSO returns the system SO the ceremony recorded, as the
// SOConfig first boot enrols, or nil when there is no record, it is
// consumed, or the store cannot read it now (read as absent, as
// AwaitPendingAdminCert reads the admin's). A record that does not decode
// is a fault, not an absence.
func PendingSystemSO(ctx context.Context, store PendingAdminCertStore) (*SOConfig, error) {
	if store == nil {
		return nil, nil
	}
	raw, err := readPendingSystemSO(ctx, store)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || bytes.Equal(raw, pendingAdminCertConsumedMarker) {
		return nil, nil
	}
	var rec pendingSystemSO
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, &PendingAdminCertStoreError{Op: "decode system SO", Cause: err}
	}
	return &SOConfig{Username: rec.Username, CertPEM: rec.CertPEM, ChainPEM: rec.ChainPEM, PoolPEM: rec.PoolPEM, PINRecord: rec.PINRecord}, nil
}

// SystemSOConsumed reports whether the pending system SO record is
// consumed: an earlier first boot enrolled the SO it named
// (Dependencies.SOEnrolled). A store that cannot read it now reports false.
func SystemSOConsumed(ctx context.Context, store PendingAdminCertStore) bool {
	if store == nil {
		return false
	}
	raw, err := store.Get(ctx, PendingSystemSOKey)
	return err == nil && bytes.Equal(raw, pendingAdminCertConsumedMarker)
}

// ClearPendingSystemSO marks the pending system SO consumed once first
// boot has enrolled it, as ClearPendingAdminCert does the admin's.
func ClearPendingSystemSO(ctx context.Context, store PendingAdminCertStore) error {
	if store == nil {
		return nil
	}
	raw, err := readPendingSystemSO(ctx, store)
	if err != nil {
		return err
	}
	if len(raw) == 0 || bytes.Equal(raw, pendingAdminCertConsumedMarker) {
		return nil
	}
	if err := store.Put(ctx, PendingSystemSOKey, pendingAdminCertConsumedMarker); err != nil {
		return &PendingAdminCertStoreError{Op: "clear system SO", Cause: err}
	}
	return nil
}

// SOPINChecker reports whether a tenant's SO PIN is committed and whether
// the committed PIN is a given record, reading and changing nothing.
// *auth.SOAuthenticator implements it.
type SOPINChecker interface {
	SOPINCommitted(ctx context.Context, tenantID string) (bool, error)
	SOPINRecordMatches(ctx context.Context, tenantID, record string) (bool, error)
}

// ClearPendingSystemSOIfEnrolled consumes the pending system SO record only
// once the SO it names is enrolled: its account exists in the system tenant
// with the so role and a live binding to the record's certificate, and the
// committed SO PIN is the record's. Until then the record is kept,
// so the next start's first boot enrols the SO; a node that only waited for
// a leader to replicate the admin may otherwise consume it before the SO
// exists, and a later strict first boot would read it as enrolled. It reports
// whether no unconsumed record remains. Nil users or pins can verify nothing
// and keep the record. A committed PIN that is not the record's, or an
// account bound only to other certificates, keeps the record and returns a
// *SystemSOUnverifiedError: a failed enrollment must not mark the SO enrolled.
func ClearPendingSystemSOIfEnrolled(ctx context.Context, store PendingAdminCertStore, users UserResolver, pins SOPINChecker) (bool, error) {
	so, err := PendingSystemSO(ctx, store)
	if err != nil {
		return false, err
	}
	if so == nil {
		return true, nil
	}
	if users == nil || pins == nil {
		return false, nil
	}
	tenantID := tenantid.System
	account, err := users.GetUserByUsername(ctx, tenantID, so.Username)
	if err != nil || account == nil || account.TenantID != tenantID ||
		!slices.Contains(account.RoleIDs, role.SystemRoleID(tenantID, role.RoleNameSO)) {
		return false, nil
	}
	committed, err := pins.SOPINCommitted(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if !committed {
		return false, nil
	}
	matches := false
	if so.PINRecord != "" {
		if matches, err = pins.SOPINRecordMatches(ctx, tenantID, so.PINRecord); err != nil {
			return false, err
		}
	}
	if !matches {
		return false, &SystemSOUnverifiedError{Username: so.Username, Reason: "the committed SO PIN is not the record's"}
	}
	thumbprint, err := spkiThumbprintFromCertPEM(so.CertPEM)
	if err != nil {
		return false, &SystemSOUnverifiedError{Username: so.Username, Reason: "the record's certificate is unreadable: " + err.Error()}
	}
	now := time.Now().UTC()
	if !slices.ContainsFunc(account.CertBindings, func(b user.CertBinding) bool {
		return b.Thumbprint == thumbprint && !b.IsExpired(now)
	}) {
		if len(account.CertBindings) == 0 {
			// Enrollment binds the certificate after it commits the PIN:
			// an unbound account may still be mid-enrollment.
			return false, nil
		}
		return false, &SystemSOUnverifiedError{Username: so.Username, Reason: "the account holds no live binding to the record's certificate"}
	}
	if err := ClearPendingSystemSO(ctx, store); err != nil {
		return false, err
	}
	return true, nil
}

// SystemSOLister lists a tenant's accounts. user.UserServicer implements it.
type SystemSOLister interface {
	ListUsers(ctx context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error)
}

// RecognizeEnrolledSystemSO records the consumed marker for a system SO
// enrolled before the marker existed. A cluster upgraded into strict
// separation of duties carries no marker and no pending record, yet may
// already hold its SO: an account in the system tenant, other than the
// bootstrap admin adminID, with the so role and a live certificate binding,
// plus a committed system SO PIN. When the persisted accounts and the PIN
// store show one, the marker is written and true returned, so a strict
// start treats the SO as enrolled instead of refusing it. A pending record
// is first boot's to enrol and is left alone; with no such SO nothing is
// written and false returned. Nil store, users or pins can verify nothing.
func RecognizeEnrolledSystemSO(ctx context.Context, store PendingAdminCertStore, users SystemSOLister, pins SOPINChecker, adminID uint64) (bool, error) {
	if store == nil || users == nil || pins == nil {
		return false, nil
	}
	raw, err := readPendingSystemSO(ctx, store)
	if err != nil {
		return false, err
	}
	if bytes.Equal(raw, pendingAdminCertConsumedMarker) {
		return true, nil
	}
	if len(raw) != 0 {
		return false, nil
	}
	tenantID := tenantid.System
	committed, err := pins.SOPINCommitted(ctx, tenantID)
	if err != nil || !committed {
		return false, err
	}
	soRole := role.SystemRoleID(tenantID, role.RoleNameSO)
	now := time.Now().UTC()
	for page := 1; ; page++ {
		resp, err := users.ListUsers(ctx, &user.ListUsersRequest{TenantID: tenantID, Page: page})
		if err != nil {
			return false, &SystemSOEnrollmentError{Op: "list accounts", Cause: err}
		}
		if resp == nil {
			return false, nil
		}
		for _, u := range resp.Users {
			if u == nil || u.ID == adminID || u.TenantID != tenantID ||
				!slices.Contains(u.RoleIDs, soRole) || user.AdminLoginBlocker(u, now) != "" {
				continue
			}
			if err := store.Put(ctx, PendingSystemSOKey, pendingAdminCertConsumedMarker); err != nil {
				return false, &PendingAdminCertStoreError{Op: "record enrolled system SO", Cause: err}
			}
			return true, nil
		}
		if !resp.HasMore || len(resp.Users) == 0 {
			return false, nil
		}
	}
}

// readPendingSystemSO reads the pending system SO record. Only not-found
// means absent (nil, nil): any other store failure is returned, so a store
// that cannot answer never reads as "no SO" and lets startup or
// consumption proceed past a record it could not see.
func readPendingSystemSO(ctx context.Context, store PendingAdminCertStore) ([]byte, error) {
	raw, err := store.Get(ctx, PendingSystemSOKey)
	if err == nil {
		return raw, nil
	}
	var notFound interface{ KeyNotFound() string }
	if errors.As(err, &notFound) {
		return nil, nil
	}
	return nil, &PendingAdminCertStoreError{Op: "read system SO", Cause: err}
}
