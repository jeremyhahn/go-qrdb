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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedLease_Grant(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	info, err := tr.LeaseGrant(context.Background(), 60)
	if err != nil {
		t.Fatalf("LeaseGrant: %v", err)
	}
	if info.ID != 42 || info.TTL != 60 {
		t.Fatalf("unexpected: %+v", info)
	}
}

func TestEmbeddedLease_Revoke(t *testing.T) {
	var revoked int64
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{
		revokeFn: func(_ context.Context, id int64) error {
			revoked = id
			return nil
		},
	}))
	if err := tr.LeaseRevoke(context.Background(), 42); err != nil {
		t.Fatalf("LeaseRevoke: %v", err)
	}
	if revoked != 42 {
		t.Fatalf("service revoked %d, want 42", revoked)
	}
}

func TestEmbeddedLease_KeepAlive(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	info, err := tr.LeaseKeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseKeepAlive: %v", err)
	}
	if info.ID != 42 {
		t.Fatalf("ID = %d", info.ID)
	}
}

func TestEmbeddedLease_TimeToLive(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	info, err := tr.LeaseTimeToLive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseTimeToLive: %v", err)
	}
	if info.ID != 42 {
		t.Fatalf("ID = %d", info.ID)
	}
}

func TestEmbeddedLease_List(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	leases, err := tr.LeaseList(context.Background())
	if err != nil {
		t.Fatalf("LeaseList: %v", err)
	}
	if len(leases) != 2 {
		t.Fatalf("got %d leases", len(leases))
	}
}

// leaseCalls drives every lease verb of tr once.
func leaseCalls(tr *Transport) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"LeaseGrant":      func() error { _, err := tr.LeaseGrant(ctx, 60); return err },
		"LeaseRevoke":     func() error { return tr.LeaseRevoke(ctx, 42) },
		"LeaseKeepAlive":  func() error { _, err := tr.LeaseKeepAlive(ctx, 42); return err },
		"LeaseTimeToLive": func() error { _, err := tr.LeaseTimeToLive(ctx, 42); return err },
		"LeaseList":       func() error { _, err := tr.LeaseList(ctx); return err },
	}
}

// Every verb answers a transport built without a lease service with the
// typed wiring-defect error.
func TestEmbeddedLease_NilServiceIsTyped(t *testing.T) {
	for name, call := range leaseCalls(newExtTransport(t)) {
		var ne *NilServiceError
		if err := call(); !errors.As(err, &ne) {
			t.Errorf("%s: err = %T (%v), want *NilServiceError", name, err, err)
		}
	}
}

// Every verb on a closed transport is refused with *transport.ClosedError
// before it reaches the service.
func TestEmbeddedLease_ClosedIsRefused(t *testing.T) {
	for name, call := range leaseCalls(closedExtTransport(t, WithLeaseService(&mockLeaseService{}))) {
		var ce *transport.ClosedError
		if err := call(); !errors.As(err, &ce) {
			t.Errorf("%s: err = %T (%v), want *transport.ClosedError", name, err, err)
		}
	}
}

// A service failure reaches the caller with the service's error still in
// its chain, from every verb.
func TestEmbeddedLease_ServiceErrorPropagates(t *testing.T) {
	failOne := func(context.Context, int64) (*lease.Lease, error) { return nil, errSentinel }
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{
		grantFn:     failOne,
		revokeFn:    func(context.Context, int64) error { return errSentinel },
		keepAliveFn: failOne,
		ttlFn:       failOne,
		leasesFn:    func(context.Context) ([]*lease.Lease, error) { return nil, errSentinel },
	}))
	for name, call := range leaseCalls(tr) {
		if err := call(); !errors.Is(err, errSentinel) {
			t.Errorf("%s: err = %v, want the service's error", name, err)
		}
	}
}

// Each lease verb reached through Request carries its typed request to the
// service and decodes the service's answer into the caller's response.
func TestEmbeddedLease_Dispatch(t *testing.T) {
	var revoked int64
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{
		revokeFn: func(_ context.Context, id int64) error {
			revoked = id
			return nil
		},
	}))
	ctx := context.Background()

	t.Run("Grant", func(t *testing.T) {
		var resp *transport.LeaseInfo
		if err := tr.Request(ctx, "LeaseGrant", &leaseGrantRequest{TTL: 60}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if resp == nil || resp.ID != 42 || resp.TTL != 60 {
			t.Fatalf("resp = %+v, want ID 42 TTL 60", resp)
		}
	})
	t.Run("Revoke", func(t *testing.T) {
		if err := tr.Request(ctx, "LeaseRevoke", &leaseRevokeRequest{LeaseID: 7}, nil); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if revoked != 7 {
			t.Fatalf("service revoked %d, want 7", revoked)
		}
	})
	t.Run("KeepAlive", func(t *testing.T) {
		var resp *transport.LeaseInfo
		if err := tr.Request(ctx, "LeaseKeepAlive", &leaseKeepAliveRequest{LeaseID: 7}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if resp == nil || resp.ID != 7 {
			t.Fatalf("resp = %+v, want ID 7", resp)
		}
	})
	t.Run("TimeToLive", func(t *testing.T) {
		var resp *transport.LeaseInfo
		if err := tr.Request(ctx, "LeaseTimeToLive", &leaseTimeToLiveRequest{LeaseID: 7}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if resp == nil || resp.ID != 7 {
			t.Fatalf("resp = %+v, want ID 7", resp)
		}
	})
	t.Run("List", func(t *testing.T) {
		var resp []*transport.LeaseInfo
		if err := tr.Request(ctx, "LeaseList", nil, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if len(resp) != 2 {
			t.Fatalf("got %d leases, want 2", len(resp))
		}
	})
}
