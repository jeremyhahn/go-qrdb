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

package errclass

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRefusal_factorConditionFailed(t *testing.T) {
	want := &auth.FactorConditionFailedError{
		UserID: 9, Type: auth.AuthFactorMTLS, Condition: auth.FactorConditionNoActiveOfType,
		CurrentVersion: 3, ExistingFactorID: "f-1",
	}
	reason, md, ok := Refusal(&wrappedError{want})
	if !ok || reason != auth.ReasonFactorConditionFailed {
		t.Fatalf("Refusal() = %q, %v; want %q, true", reason, ok, auth.ReasonFactorConditionFailed)
	}
	if got := auth.FactorConditionFailedFromMetadata(md); *got != *want {
		t.Fatalf("metadata decodes to %+v, want %+v", got, want)
	}
}

func TestRefusal_userConcurrentUpdate(t *testing.T) {
	want := &user.ConcurrentUpdateError{ID: 9}
	reason, md, ok := Refusal(&wrappedError{want})
	if !ok || reason != transport.ReasonUserConcurrentUpdate {
		t.Fatalf("Refusal() = %q, %v; want %q, true", reason, ok, transport.ReasonUserConcurrentUpdate)
	}
	if got := transport.UserConcurrentUpdateFromMetadata(md); *got != *want {
		t.Fatalf("metadata decodes to %+v, want %+v", got, want)
	}
	if got := Classify(want); got != Aborted {
		t.Fatalf("Classify(lost race) = %v, want Aborted", got)
	}
}

func TestRefusal_barrierSentinels(t *testing.T) {
	cases := map[error]string{
		seal.ErrNotInitialized:           transport.ReasonBarrierNotInitialized,
		seal.ErrCredentialMismatch:       transport.ReasonBarrierCredentialMismatch,
		seal.ErrPassphraseChangeConflict: transport.ReasonBarrierPassphraseChangeConflict,
		seal.ErrRootKeyWriteInProgress:   transport.ReasonBarrierRootKeyWriteInProgress,
	}
	for sentinel, want := range cases {
		reason, md, ok := Refusal(&wrappedError{sentinel})
		if !ok || reason != want || md != nil {
			t.Fatalf("Refusal(%v) = %q, %v, %v; want %q, nil, true", sentinel, reason, md, ok, want)
		}
		if got := transport.RefusalError(reason, md); !errors.Is(got, sentinel) {
			t.Fatalf("RefusalError(%q) = %v, want %v", reason, got, sentinel)
		}
	}
}

// TestRefusal_vlog checks each verifiable-log refusal a client rebuilds typed
// carries its reason and metadata through a wrap, and keeps its class.
func TestRefusal_vlog(t *testing.T) {
	id := vlog.LogID{TenantID: "acme", Database: "ledger", Log: "certs"}
	cases := []struct {
		err    error
		reason string
		class  Class
	}{
		{&vlog.LogExistsError{ID: id}, vlog.ReasonLogExists, AlreadyExists},
		{&vlog.CheckpointExistsError{Size: 5}, vlog.ReasonCheckpointExists, AlreadyExists},
		{&vlog.ExpireNotAllowedError{ID: id}, vlog.ReasonExpireNotAllowed, FailedPrecondition},
	}
	for _, c := range cases {
		reason, md, ok := Refusal(&wrappedError{c.err})
		if !ok || reason != c.reason {
			t.Fatalf("Refusal(%T) = %q, %v; want %q, true", c.err, reason, ok, c.reason)
		}
		if got := transport.RefusalError(reason, md); !reflect.DeepEqual(got, c.err) {
			t.Fatalf("Refusal(%T) rebuilds %#v, want %#v", c.err, got, c.err)
		}
		if got := Classify(c.err); got != c.class {
			t.Fatalf("Classify(%T) = %v, want %v", c.err, got, c.class)
		}
	}
}

// A failed rollback over a lost race is DataLoss, not a conflict to retry, so
// it carries no refusal a client would rebuild as one.
func TestRefusal_userConcurrentUpdateUnderDataLossCarriesNone(t *testing.T) {
	err := &user.IndexCompensationError{Op: "update", Cause: &user.ConcurrentUpdateError{ID: 9}}
	if got := Classify(err); got != DataLoss {
		t.Fatalf("Classify() = %v, want DataLoss", got)
	}
	if reason, md, ok := Refusal(err); ok || reason != "" || md != nil {
		t.Fatalf("Refusal() = %q, %v, %v; want none", reason, md, ok)
	}
}

func TestRefusal_adminCredentialRequiresCA(t *testing.T) {
	want := &ceremony.AdminCredentialRequiresCAError{Path: "generate"}
	reason, md, ok := Refusal(&wrappedError{want})
	if !ok || reason != ceremony.ReasonAdminCredentialRequiresCA {
		t.Fatalf("Refusal() = %q, %v; want %q, true", reason, ok, ceremony.ReasonAdminCredentialRequiresCA)
	}
	if got := ceremony.AdminCredentialRequiresCAFromMetadata(md); *got != *want {
		t.Fatalf("metadata decodes to %+v, want %+v", got, want)
	}
	if got := Classify(want); got != FailedPrecondition {
		t.Fatalf("Classify(no CA) = %v, want FailedPrecondition", got)
	}
}

func TestRefusal_replicaRefusalsUnchanged(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"not a replica": {err: &placement.NotAReplicaError{TenantID: "t", Database: "d"}, want: replicaforward.ReasonNotAReplica},
		"not leader":    {err: &service.NotLeaderError{LeaderAddress: "leader.example.com:7000"}, want: replicaforward.ReasonNotLeader},
		"lease ttl on a follower": {
			err:  &service.LeaseTTLNotLeaderError{NotLeaderError: service.NotLeaderError{LeaderAddress: "leader.example.com:7000"}},
			want: replicaforward.ReasonNotLeader,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wantReason, wantMD, _ := replicaforward.Refusal(tc.err)
			reason, md, ok := Refusal(tc.err)
			if !ok || reason != tc.want || reason != wantReason || len(md) != len(wantMD) {
				t.Fatalf("Refusal() = %q, %v, %v; want %q with %v", reason, md, ok, tc.want, wantMD)
			}
		})
	}
}

func TestRefusal_otherErrorsCarryNone(t *testing.T) {
	for _, err := range []error{nil, errors.New("boom"), auth.ErrFactorAlreadyExists, service.ErrFactorSetContended} {
		if reason, md, ok := Refusal(err); ok || reason != "" || md != nil {
			t.Fatalf("Refusal(%v) = %q, %v, %v; want none", err, reason, md, ok)
		}
	}
}

func TestRefusal_vectorNamespaceNotFound(t *testing.T) {
	for _, ns := range []string{"docs", ""} {
		reason, md, ok := Refusal(&wrappedError{&service.NamespaceNotFoundError{Namespace: ns}})
		if !ok || reason != transport.ReasonVectorNamespaceNotFound {
			t.Fatalf("Refusal() = %q, %v; want %q, true", reason, ok, transport.ReasonVectorNamespaceNotFound)
		}
		want := &transport.NamespaceNotFoundError{Namespace: ns}
		if got := transport.NamespaceNotFoundFromMetadata(md); *got != *want {
			t.Fatalf("metadata decodes to %+v, want %+v", got, want)
		}
	}
}

func TestRefusal_vectorDimensionMismatch(t *testing.T) {
	for _, e := range []*service.DimensionMismatchError{{Expected: 768, Actual: 384}, {Expected: 3, Actual: 0}} {
		reason, md, ok := Refusal(&wrappedError{e})
		if !ok || reason != transport.ReasonVectorDimensionMismatch {
			t.Fatalf("Refusal() = %q, %v; want %q, true", reason, ok, transport.ReasonVectorDimensionMismatch)
		}
		want := &transport.DimensionMismatchError{Expected: e.Expected, Got: e.Actual}
		if got := transport.DimensionMismatchFromMetadata(md); *got != *want {
			t.Fatalf("metadata decodes to %+v, want %+v", got, want)
		}
	}
}

func TestVectorRefusal_otherErrorsCarryNone(t *testing.T) {
	for _, err := range []error{errors.New("plain"), &service.NamespaceExistsError{Namespace: "docs"}} {
		if reason, md, ok := vectorRefusal(err); ok || reason != "" || md != nil {
			t.Fatalf("vectorRefusal(%v) = %q, %v, %v; want none", err, reason, md, ok)
		}
	}
}

func TestClassify_factorConditionAndContention(t *testing.T) {
	if got := Classify(&auth.FactorConditionFailedError{}); got != AlreadyExists {
		t.Fatalf("Classify(condition failed) = %v, want AlreadyExists", got)
	}
	if got := Classify(&service.FactorSetContendedError{UserID: 1, Attempts: 64}); got != Aborted {
		t.Fatalf("Classify(contended) = %v, want Aborted", got)
	}
}

// A follower's TimeToLive refusal is as unavailable as any not-leader refusal
// where no transport forwards it.
func TestClassify_leaseTTLOnAFollowerIsUnavailable(t *testing.T) {
	if got := Classify(&service.LeaseTTLNotLeaderError{}); got != Unavailable {
		t.Fatalf("Classify(lease ttl not leader) = %v, want Unavailable", got)
	}
}
