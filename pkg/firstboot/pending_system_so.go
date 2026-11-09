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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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
		Username: so.Username, CertPEM: so.CertPEM, ChainPEM: so.ChainPEM, PINRecord: so.PINRecord,
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
	return &SOConfig{Username: rec.Username, CertPEM: rec.CertPEM, ChainPEM: rec.ChainPEM, PINRecord: rec.PINRecord}, nil
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
