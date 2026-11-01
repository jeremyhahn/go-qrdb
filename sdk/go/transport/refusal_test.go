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

package transport

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

func TestRefusalError_factorConditionFailed(t *testing.T) {
	want := &auth.FactorConditionFailedError{
		UserID: 4, Type: auth.AuthFactorFIDO2, Condition: auth.FactorConditionExpectedVersion,
		ExpectedVersion: 1, CurrentVersion: 2,
	}
	err := RefusalError(auth.ReasonFactorConditionFailed, want.Metadata())
	if !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("RefusalError() = %v, want ErrFactorConditionFailed", err)
	}
	got, ok := errors.AsType[*auth.FactorConditionFailedError](err)
	if !ok || *got != *want {
		t.Fatalf("RefusalError() = %+v, want %+v", got, want)
	}
}

func TestRefusalError_unknownReasonIsNil(t *testing.T) {
	for _, reason := range []string{"", "NOT_A_REPLICA", "NOT_LEADER", "SOMETHING_NEWER"} {
		if err := RefusalError(reason, map[string]string{"k": "v"}); err != nil {
			t.Fatalf("RefusalError(%q) = %v, want nil", reason, err)
		}
	}
}

func TestBarrierRefusal_roundTripsEverySentinel(t *testing.T) {
	for _, sentinel := range []error{seal.ErrNotInitialized, seal.ErrCredentialMismatch, seal.ErrPassphraseChangeConflict, seal.ErrRootKeyWriteInProgress} {
		reason, ok := BarrierRefusal(errors.Join(errors.New("context"), sentinel))
		if !ok || reason == "" {
			t.Fatalf("BarrierRefusal(%v) = %q, %v; want a reason", sentinel, reason, ok)
		}
		if got := RefusalError(reason, nil); !errors.Is(got, sentinel) {
			t.Fatalf("RefusalError(%q) = %v, want %v", reason, got, sentinel)
		}
	}
}

func TestBarrierRefusal_otherErrorsCarryNone(t *testing.T) {
	for _, err := range []error{nil, errors.New("other"), seal.ErrSealed} {
		if reason, ok := BarrierRefusal(err); ok {
			t.Fatalf("BarrierRefusal(%v) = %q, want none", err, reason)
		}
	}
}

func TestRefusalError_userConcurrentUpdate(t *testing.T) {
	want := &UserConcurrentUpdateError{ID: 17}
	err := RefusalError(ReasonUserConcurrentUpdate, want.Metadata())
	got, ok := errors.AsType[*UserConcurrentUpdateError](err)
	if !ok || *got != *want {
		t.Fatalf("RefusalError() = %+v, want %+v", got, want)
	}
	if !dberrors.Is(err, dberrors.ErrConflict) || !dberrors.IsRetryable(err) {
		t.Fatalf("RefusalError() = %v, want a retryable ErrConflict", err)
	}
}

// TestRefusalError_lockOutcomes pins that each lock refusal rebuilds the
// typed lock error a caller matches with errors.As, fields included.
func TestRefusalError_lockOutcomes(t *testing.T) {
	held := &lock.LockHeldError{Key: "k", HolderID: "h"}
	if got, ok := errors.AsType[*lock.LockHeldError](RefusalError(lock.ReasonLockHeld, held.Metadata())); !ok || *got != *held {
		t.Fatalf("RefusalError(held) = %+v, want %+v", got, held)
	}
	notHolder := &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4}
	if got, ok := errors.AsType[*lock.NotLockHolderError](RefusalError(lock.ReasonNotLockHolder, notHolder.Metadata())); !ok || *got != *notHolder {
		t.Fatalf("RefusalError(not holder) = %+v, want %+v", got, notHolder)
	}
	notFound := &lock.LockNotFoundError{Key: "k"}
	if got, ok := errors.AsType[*lock.LockNotFoundError](RefusalError(lock.ReasonLockNotFound, notFound.Metadata())); !ok || *got != *notFound {
		t.Fatalf("RefusalError(not found) = %+v, want %+v", got, notFound)
	}
}

// TestRefusalError_lockOutcomeWithoutMetadataKeepsItsType pins that a lock
// refusal whose metadata is missing still rebuilds the typed error: the
// reason, not the metadata, carries the outcome.
func TestRefusalError_lockOutcomeWithoutMetadataKeepsItsType(t *testing.T) {
	if _, ok := errors.AsType[*lock.LockNotFoundError](RefusalError(lock.ReasonLockNotFound, nil)); !ok {
		t.Fatal("RefusalError(LOCK_NOT_FOUND, nil) is not a *lock.LockNotFoundError")
	}
	if _, ok := errors.AsType[*lock.NotLockHolderError](RefusalError(lock.ReasonNotLockHolder, nil)); !ok {
		t.Fatal("RefusalError(NOT_LOCK_HOLDER, nil) is not a *lock.NotLockHolderError")
	}
}

func TestRefusalError_adminCredentialRequiresCA(t *testing.T) {
	for _, path := range []string{"csr-sign", "generate"} {
		want := &ceremony.AdminCredentialRequiresCAError{Path: path}
		err := RefusalError(ceremony.ReasonAdminCredentialRequiresCA, want.Metadata())
		got, ok := errors.AsType[*ceremony.AdminCredentialRequiresCAError](err)
		if !ok || *got != *want {
			t.Fatalf("RefusalError() = %+v, want %+v", got, want)
		}
	}
}

// TestRefusalError_vlog checks every verifiable-log refusal reason rebuilds
// its typed vlog error, metadata and all (go-qrdb#912).
func TestRefusalError_vlog(t *testing.T) {
	id := vlog.LogID{TenantID: "acme", Database: "ledger", Log: "certs"}
	exists := &vlog.LogExistsError{ID: id}
	checkpoint := &vlog.CheckpointExistsError{Size: 12}
	expire := &vlog.ExpireNotAllowedError{ID: id}
	for reason, want := range map[string]interface {
		error
		Metadata() map[string]string
	}{
		vlog.ReasonLogExists:        exists,
		vlog.ReasonCheckpointExists: checkpoint,
		vlog.ReasonExpireNotAllowed: expire,
	} {
		if got := RefusalError(reason, want.Metadata()); !reflect.DeepEqual(got, want) {
			t.Fatalf("RefusalError(%q) = %#v, want %#v", reason, got, want)
		}
		if got := RefusalError(reason, nil); reflect.TypeOf(got) != reflect.TypeOf(want) {
			t.Fatalf("RefusalError(%q, nil) = %T, want %T", reason, got, want)
		}
	}
}

func TestUserConcurrentUpdateFromMetadata_malformedIDKeepsClassification(t *testing.T) {
	for _, md := range []map[string]string{nil, {"user_id": "x"}, {"user_id": "-1"}, {"user_id": "99999999999999999999"}} {
		if got := UserConcurrentUpdateFromMetadata(md); got == nil || got.ID != 0 {
			t.Fatalf("UserConcurrentUpdateFromMetadata(%v) = %+v, want ID 0", md, got)
		}
	}
}

func TestUserConcurrentUpdateError_isRetryableConflict(t *testing.T) {
	var err error = &UserConcurrentUpdateError{ID: 9}
	if want := "user: concurrent update to user 9; re-read and retry"; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	qe, ok := errors.AsType[*dberrors.QRDBError](err)
	if !ok || qe.Code != dberrors.ErrConflict || !qe.Retry {
		t.Fatalf("As QRDBError = %+v, want a retryable ErrConflict", qe)
	}
	if inner, ok := errors.AsType[*UserConcurrentUpdateError](qe); !ok || inner.ID != 9 {
		t.Fatalf("QRDBError unwraps to %+v, want the lost race of user 9", inner)
	}
	if (&UserConcurrentUpdateError{}).As(new(*UserNotFoundError)) {
		t.Fatal("As matched a target that is not a QRDBError")
	}
}

func TestAuthFactorAddRequest_Condition(t *testing.T) {
	v := uint64(0)
	cases := map[string]struct {
		req  AuthFactorAddRequest
		want *auth.FactorAddCondition
	}{
		"plain":            {req: AuthFactorAddRequest{}},
		"no active type":   {req: AuthFactorAddRequest{IfNoActiveType: true}, want: &auth.FactorAddCondition{NoActiveOfType: true}},
		"expected version": {req: AuthFactorAddRequest{ExpectedFactorSetVersion: &v}, want: &auth.FactorAddCondition{ExpectedVersion: &v}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.req.Condition()
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("Condition() = %+v, want %+v", got, tc.want)
			}
			if got != nil && (got.NoActiveOfType != tc.want.NoActiveOfType || got.ExpectedVersion != tc.want.ExpectedVersion) {
				t.Fatalf("Condition() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAuthFactorRotateRequest_Validate(t *testing.T) {
	plain := AuthFactorRotateRequest{OldFactorID: "f-1", NewFactor: AuthFactorAddRequest{Type: "mtls"}}
	if err := plain.Validate(); err != nil {
		t.Fatalf("plain rotation: err = %v, want nil", err)
	}
	v := uint64(2)
	for name, nf := range map[string]AuthFactorAddRequest{
		"no active type":   {IfNoActiveType: true},
		"expected version": {ExpectedFactorSetVersion: &v},
	} {
		req := AuthFactorRotateRequest{OldFactorID: "f-1", NewFactor: nf}
		if err := req.Validate(); !errors.Is(err, auth.ErrInvalidFactor) {
			t.Fatalf("%s: err = %v, want ErrInvalidFactor", name, err)
		}
	}
}
