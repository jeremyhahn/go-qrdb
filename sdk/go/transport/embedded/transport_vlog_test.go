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

//go:build qrdb_vlog

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// vlogTransport is an embedded transport over a working log service, and a
// context in tenant acme.
func vlogTransport(t *testing.T) (*Transport, context.Context) {
	t.Helper()
	return newExtTransport(t, WithLogService(servicetest.NewLogService(t))), tenant.WithTenantID(context.Background(), "acme")
}

func TestVlog_EveryMethodRoundTripsThroughTheService(t *testing.T) {
	tr, ctx := vlogTransport(t)

	created, err := tr.VlogCreate(ctx, &vlogwire.CreateRequest{
		Log: "audit", Origin: servicetest.LogOrigin, AllowExpireBodies: true, SigningKeyType: vlog.SignatureEd25519,
	})
	if err != nil {
		t.Fatalf("VlogCreate: %v", err)
	}
	if created.TenantID != "acme" || created.Database != "system" || created.Log != "audit" || created.Key == nil || created.Key.Version != 1 {
		t.Fatalf("VlogCreate = %+v, want acme/system/audit with key version 1", created)
	}

	appended, err := tr.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "audit", Entries: [][]byte{[]byte("a"), []byte("b"), []byte("c")}})
	if err != nil || appended.FirstIndex != 0 || appended.Count != 3 {
		t.Fatalf("VlogAppend = %+v, %v; want first 0, count 3", appended, err)
	}

	got, err := tr.VlogGet(ctx, &vlogwire.GetRequest{Log: "audit", From: 1, Limit: 2})
	if err != nil || len(got.Entries) != 2 || string(got.Entries[0].Body) != "b" || got.Entries[1].Index != 2 {
		t.Fatalf("VlogGet = %+v, %v; want entries 1 and 2", got, err)
	}

	list, err := tr.VlogList(ctx, &vlogwire.ListRequest{})
	if err != nil || len(list.Logs) != 1 || list.Logs[0] != "audit" {
		t.Fatalf("VlogList = %+v, %v; want [audit]", list, err)
	}

	head, err := tr.VlogHead(ctx, &vlogwire.LogRequest{Log: "audit"})
	if err != nil || head.Size != 3 || len(head.Root) == 0 {
		t.Fatalf("VlogHead = %+v, %v; want size 3 with a root", head, err)
	}

	incl, err := tr.VlogProveInclusion(ctx, &vlogwire.InclusionRequest{Log: "audit", Index: 1, Size: 3})
	if err != nil || incl.Index != 1 || incl.Size != 3 || len(incl.Hashes) == 0 {
		t.Fatalf("VlogProveInclusion = %+v, %v", incl, err)
	}

	cons, err := tr.VlogProveConsistency(ctx, &vlogwire.ConsistencyRequest{Log: "audit", Size1: 1, Size2: 3})
	if err != nil || cons.Size1 != 1 || cons.Size2 != 3 || len(cons.Hashes) == 0 {
		t.Fatalf("VlogProveConsistency = %+v, %v", cons, err)
	}

	signed, err := tr.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "audit"})
	if err != nil || signed.Head.Size != 3 || signed.Head.KeyVersion != 1 {
		t.Fatalf("VlogSignHead = %+v, %v", signed, err)
	}
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, signed.Checkpoint, vlog.HeadKey{Version: created.Key.Version, PublicKey: created.Key.PublicKey})

	rotated, err := tr.VlogRotateKey(ctx, &vlogwire.RotateKeyRequest{Log: "audit", SigningKeyType: vlog.SignatureECDSAP256SHA256})
	if err != nil || rotated.Key.Version != 2 || len(rotated.Key.PublicKey) == 0 {
		t.Fatalf("VlogRotateKey = %+v, %v; want key version 2", rotated, err)
	}
	if _, err := tr.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "audit", Entries: [][]byte{[]byte("d")}}); err != nil {
		t.Fatalf("VlogAppend after rotate: %v", err)
	}
	resigned, err := tr.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "audit"})
	if err != nil {
		t.Fatalf("VlogSignHead after rotate: %v", err)
	}
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, resigned.Checkpoint, vlog.HeadKey{Version: rotated.Key.Version, PublicKey: rotated.Key.PublicKey})

	expired, err := tr.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "audit", Cutoff: 2})
	if err != nil || expired.Expired != 2 {
		t.Fatalf("VlogExpireBodies = %+v, %v; want 2 expired", expired, err)
	}
	after, err := tr.VlogGet(ctx, &vlogwire.GetRequest{Log: "audit"})
	if err != nil || len(after.Entries) != 4 || !after.Entries[1].Expired || after.Entries[2].Expired {
		t.Fatalf("VlogGet after expiry = %+v, %v; want entries 0 and 1 expired", after, err)
	}
}

func TestVlog_ServiceErrorsKeepTheirType(t *testing.T) {
	tr, ctx := vlogTransport(t)
	_, err := tr.VlogHead(ctx, &vlogwire.LogRequest{Log: "absent"})
	var nf *vlog.LogNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("VlogHead of an absent log: err = %v (%T), want *vlog.LogNotFoundError", err, err)
	}
	// A write to an absent log is refused with the same typed error a read
	// of it returns, whichever side of the proposal catches it.
	writes := map[string]func() error{
		"VlogAppend": func() error {
			_, err := tr.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "absent", Entries: [][]byte{[]byte("x")}})
			return err
		},
		"VlogSignHead": func() error {
			_, err := tr.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "absent"})
			return err
		},
		"VlogExpireBodies": func() error {
			_, err := tr.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "absent", Cutoff: 1})
			return err
		},
	}
	for name, write := range writes {
		err := write()
		nf = nil
		if !errors.As(err, &nf) || nf.ID.Log != "absent" {
			t.Fatalf("%s to an absent log: err = %v (%T), want *vlog.LogNotFoundError naming it", name, err, err)
		}
	}
}

// TestVlog_RefusalsKeepTheirType checks the state machine's three refusals
// reach an embedded caller as their typed vlog errors (go-qrdb#912).
func TestVlog_RefusalsKeepTheirType(t *testing.T) {
	tr, ctx := vlogTransport(t)
	if _, err := tr.VlogCreate(ctx, &vlogwire.CreateRequest{Log: "audit", Origin: servicetest.LogOrigin, SigningKeyType: vlog.SignatureEd25519}); err != nil {
		t.Fatalf("VlogCreate: %v", err)
	}
	if _, err := tr.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "audit", Entries: [][]byte{[]byte("a")}}); err != nil {
		t.Fatalf("VlogAppend: %v", err)
	}
	audit := vlog.LogID{TenantID: "acme", Database: "system", Log: "audit"}

	_, err := tr.VlogCreate(ctx, &vlogwire.CreateRequest{Log: "audit", Origin: servicetest.LogOrigin})
	if exists := (*vlog.LogExistsError)(nil); !errors.As(err, &exists) || exists.ID != audit {
		t.Fatalf("second VlogCreate: err = %v (%T), want *vlog.LogExistsError naming %+v", err, err, audit)
	}
	if _, err := tr.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "audit"}); err != nil {
		t.Fatalf("VlogSignHead: %v", err)
	}
	_, err = tr.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "audit"})
	if exists := (*vlog.CheckpointExistsError)(nil); !errors.As(err, &exists) || exists.Size != 1 {
		t.Fatalf("second VlogSignHead: err = %v (%T), want *vlog.CheckpointExistsError at size 1", err, err)
	}
	_, err = tr.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "audit", Cutoff: 1})
	if refused := (*vlog.ExpireNotAllowedError)(nil); !errors.As(err, &refused) || refused.ID != audit {
		t.Fatalf("VlogExpireBodies: err = %v (%T), want *vlog.ExpireNotAllowedError naming %+v", err, err, audit)
	}
}

func TestVlog_ScopedTransportsKeepTheirLogsApart(t *testing.T) {
	svc := servicetest.NewLogService(t)
	tr := newExtTransport(t, WithLogService(svc), WithServiceRegistry(newScopeRegistry()))
	ctx := tenant.WithTenantID(context.Background(), "acme")
	scoped, err := tr.WithDatabase("orders")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	orders := scoped.(*Transport)
	if _, err := orders.VlogCreate(ctx, &vlogwire.CreateRequest{Log: "audit", Origin: servicetest.LogOrigin}); err != nil {
		t.Fatalf("VlogCreate in orders: %v", err)
	}
	if list, err := orders.VlogList(ctx, nil); err != nil || len(list.Logs) != 1 {
		t.Fatalf("orders VlogList = %+v, %v; want one log", list, err)
	}
	if list, err := tr.VlogList(ctx, nil); err != nil || len(list.Logs) != 0 {
		t.Fatalf("system VlogList = %+v, %v; want none", list, err)
	}
}
