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

package joinadvertise_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/joinadvertise"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newTestServices builds a raft-free lease + MVCC service pair over an
// in-memory local engine -- the same recipe
// TestStartLocalLeaseExpiry_DeletesAnExpiredLeasesKeys uses -- so
// Advertiser's write path is exercised against the REAL service
// implementations, not a hand-rolled double standing in for them.
func newTestServices(t *testing.T) (service.LeaseServicer, service.MVCCKVServicer) {
	t.Helper()
	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendMemory, t.TempDir(), nil, qrdbconfig.ObjstoreConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	lr := lease.NewLessor()
	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, pdb, nil, lr)
	if err != nil {
		t.Fatalf("NewLocalEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	return service.NewLeaseService(host, 1), service.NewMVCCKVService(host, 1)
}

func TestNew_NilWithoutBothServices(t *testing.T) {
	leaseSvc, mvccSvc := newTestServices(t)

	if joinadvertise.New(nil, mvccSvc) != nil {
		t.Error("New(nil, mvccSvc) should be nil")
	}
	if joinadvertise.New(leaseSvc, nil) != nil {
		t.Error("New(leaseSvc, nil) should be nil")
	}
	if joinadvertise.New(nil, nil) != nil {
		t.Error("New(nil, nil) should be nil")
	}
	if joinadvertise.New(leaseSvc, mvccSvc) == nil {
		t.Error("New(leaseSvc, mvccSvc) should not be nil")
	}
}

func TestAdvertiseJoiner_WritesALeaseBackedRecordSupportsReads(t *testing.T) {
	leaseSvc, mvccSvc := newTestServices(t)
	a := joinadvertise.New(leaseSvc, mvccSvc)

	ctx := context.Background()
	err := a.AdvertiseJoiner(ctx, 4, "10.0.0.4:63001", featuregate.SupportedNames())
	if err != nil {
		t.Fatalf("AdvertiseJoiner: %v", err)
	}

	kv, err := mvccSvc.GetRevision(ctx, featuregate.AdvertisementPrefix+"4")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	var member cluster.Member
	if err := json.Unmarshal(kv.Value, &member); err != nil {
		t.Fatalf("unmarshal advertised record: %v", err)
	}
	if member.NodeID != 4 {
		t.Errorf("NodeID = %d, want 4", member.NodeID)
	}
	if member.Address != "10.0.0.4:63001" {
		t.Errorf("Address = %q, want %q", member.Address, "10.0.0.4:63001")
	}
	for _, f := range featuregate.SupportedNames() {
		found := false
		for _, got := range member.Features {
			if got == f {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Features = %v, want to contain %q", member.Features, f)
		}
	}
}

func TestAdvertiseJoiner_DropsUnknownFeatureNames(t *testing.T) {
	leaseSvc, mvccSvc := newTestServices(t)
	a := joinadvertise.New(leaseSvc, mvccSvc)

	ctx := context.Background()
	claimed := append([]string{"a-feature-this-binary-has-never-heard-of"}, featuregate.SupportedNames()...)
	if err := a.AdvertiseJoiner(ctx, 7, "10.0.0.7:63001", claimed); err != nil {
		t.Fatalf("AdvertiseJoiner: %v", err)
	}

	kv, err := mvccSvc.GetRevision(ctx, featuregate.AdvertisementPrefix+"7")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	var member cluster.Member
	if err := json.Unmarshal(kv.Value, &member); err != nil {
		t.Fatalf("unmarshal advertised record: %v", err)
	}
	for _, got := range member.Features {
		if got == "a-feature-this-binary-has-never-heard-of" {
			t.Fatalf("Features = %v, must not carry an unknown name onto the wire", member.Features)
		}
	}
}

func TestAdvertiseJoiner_ZeroFeaturesWritesAnEmptyAdvertisement(t *testing.T) {
	leaseSvc, mvccSvc := newTestServices(t)
	a := joinadvertise.New(leaseSvc, mvccSvc)

	ctx := context.Background()
	if err := a.AdvertiseJoiner(ctx, 9, "10.0.0.9:63001", nil); err != nil {
		t.Fatalf("AdvertiseJoiner: %v", err)
	}

	kv, err := mvccSvc.GetRevision(ctx, featuregate.AdvertisementPrefix+"9")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	var member cluster.Member
	if err := json.Unmarshal(kv.Value, &member); err != nil {
		t.Fatalf("unmarshal advertised record: %v", err)
	}
	if len(member.Features) != 0 {
		t.Errorf("Features = %v, want none", member.Features)
	}
}

// fakeLeaseServicer wraps a real LeaseServicer but fails every Grant, to
// exercise AdvertiseJoiner's lease-failure path without needing a second
// kind of real backend fault.
type fakeLeaseServicer struct {
	service.LeaseServicer
}

func (f *fakeLeaseServicer) Grant(context.Context, int64) (*lease.Lease, error) {
	return nil, errors.New("lease grant unavailable")
}

func TestAdvertiseJoiner_LeaseGrantFailure(t *testing.T) {
	_, mvccSvc := newTestServices(t)
	a := joinadvertise.New(&fakeLeaseServicer{}, mvccSvc)

	err := a.AdvertiseJoiner(context.Background(), 4, "10.0.0.4:63001", nil)
	var leaseErr *joinadvertise.LeaseError
	if !errors.As(err, &leaseErr) {
		t.Fatalf("error = %T (%v), want *joinadvertise.LeaseError", err, err)
	}
}

// fakeMVCCServicer wraps a real MVCCKVServicer but fails every PutWithLease,
// to exercise AdvertiseJoiner's write-failure (and lease-revoke-on-failure)
// path.
type fakeMVCCServicer struct {
	service.MVCCKVServicer
}

func (f *fakeMVCCServicer) PutWithLease(context.Context, string, []byte, int64) (*service.KeyValue, error) {
	return nil, errors.New("write unavailable")
}

func TestAdvertiseJoiner_WriteFailureRevokesTheLease(t *testing.T) {
	leaseSvc, mvccSvc := newTestServices(t)
	a := joinadvertise.New(leaseSvc, &fakeMVCCServicer{MVCCKVServicer: mvccSvc})

	err := a.AdvertiseJoiner(context.Background(), 4, "10.0.0.4:63001", nil)
	var writeErr *joinadvertise.WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("error = %T (%v), want *joinadvertise.WriteError", err, err)
	}

	// The lease this attempt granted must not survive the failed write --
	// confirmed by its absence rather than by capturing the lease ID, since
	// this test only has the real LeaseServicer's own accounting to ask.
	leases, err := leaseSvc.Leases(context.Background())
	if err != nil {
		t.Fatalf("Leases: %v", err)
	}
	if len(leases) != 0 {
		t.Errorf("Leases() = %v, want none (the failed write's lease should have been revoked)", leases)
	}
}

func TestErrorTypes_WrapAndDescribeTheUnderlyingCause(t *testing.T) {
	cause := errors.New("underlying failure")

	cases := []struct {
		name string
		err  interface {
			error
			Unwrap() error
		}
	}{
		{"EncodeError", &joinadvertise.EncodeError{Err: cause}},
		{"LeaseError", &joinadvertise.LeaseError{Err: cause}},
		{"WriteError", &joinadvertise.WriteError{Err: cause}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Error() == "" {
				t.Error("Error() returned an empty string")
			}
			if !errors.Is(c.err, cause) {
				t.Errorf("Unwrap() did not expose the underlying cause")
			}
		})
	}
}

func TestDefaultTTL_IsPositiveAndBoundedByTheEnrollTimeoutWindow(t *testing.T) {
	if joinadvertise.DefaultTTL <= 0 {
		t.Fatalf("DefaultTTL = %v, want > 0", joinadvertise.DefaultTTL)
	}
	if joinadvertise.DefaultTTL > time.Hour {
		t.Fatalf("DefaultTTL = %v, want a short admission window, not hours", joinadvertise.DefaultTTL)
	}
}
