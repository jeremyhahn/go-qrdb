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

package vlogtest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// NewService is the in-memory log service the transport's server is built
// over.
func NewService(t *testing.T) service.LogService {
	t.Helper()
	return servicetest.NewLogService(t)
}

// Run drives a log through every VlogClient method against a server over a
// service from NewService, in the default database: create with a key,
// append, read, list, both proofs, sign and verify the checkpoint, rotate
// the key and verify under the new one, expire bodies, and an absent log.
func Run(t *testing.T, c transport.VlogClient) {
	t.Helper()
	ctx := context.Background()

	created, err := c.VlogCreate(ctx, &vlogwire.CreateRequest{
		Log: "ledger", Origin: servicetest.LogOrigin, AllowExpireBodies: true, SigningKeyType: vlog.SignatureEd25519,
	})
	if err != nil {
		t.Fatalf("VlogCreate: %v", err)
	}
	if created.Database != transport.DefaultDatabaseScope || created.Log != "ledger" || created.TenantID == "" || created.Key == nil || created.Key.Version != 1 || len(created.Key.PublicKey) == 0 {
		t.Fatalf("VlogCreate = %+v, want ledger in the default database with key version 1", created)
	}

	first, err := c.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "ledger", Entries: [][]byte{[]byte("a")}})
	if err != nil || *first != (vlogwire.AppendResult{FirstIndex: 0, Count: 1}) {
		t.Fatalf("VlogAppend = %+v, %v; want first 0, count 1", first, err)
	}
	head1, err := c.VlogHead(ctx, &vlogwire.LogRequest{Log: "ledger"})
	if err != nil || head1.Size != 1 {
		t.Fatalf("VlogHead = %+v, %v; want size 1", head1, err)
	}
	appended, err := c.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "ledger", Entries: [][]byte{[]byte("b"), []byte("c")}})
	if err != nil || *appended != (vlogwire.AppendResult{FirstIndex: 1, Count: 2}) {
		t.Fatalf("VlogAppend = %+v, %v; want first 1, count 2", appended, err)
	}

	got, err := c.VlogGet(ctx, &vlogwire.GetRequest{Log: "ledger", From: 1, Limit: 2})
	want := []vlogwire.Entry{{Index: 1, Body: []byte("b")}, {Index: 2, Body: []byte("c")}}
	if err != nil || !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("VlogGet = %+v, %v; want %+v", got, err, want)
	}

	list, err := c.VlogList(ctx, &vlogwire.ListRequest{})
	if err != nil || !reflect.DeepEqual(list.Logs, []string{"ledger"}) {
		t.Fatalf("VlogList = %+v, %v; want [ledger]", list, err)
	}

	head, err := c.VlogHead(ctx, &vlogwire.LogRequest{Log: "ledger"})
	if err != nil || head.Size != 3 || len(head.Root) == 0 {
		t.Fatalf("VlogHead = %+v, %v; want size 3 with a root", head, err)
	}

	incl, err := c.VlogProveInclusion(ctx, &vlogwire.InclusionRequest{Log: "ledger", Index: 1, Size: 3})
	if err != nil || incl.Index != 1 || incl.Size != 3 || len(incl.Hashes) == 0 {
		t.Fatalf("VlogProveInclusion = %+v, %v", incl, err)
	}
	// The proofs must verify, from the caller's side of the wire, against
	// the roots the log's heads answer (go-qrdb#1194).
	if err := servicetest.VerifyInclusion([]byte("b"), incl.Index, incl.Size, incl.Hashes, head.Root); err != nil {
		t.Fatalf("VlogProveInclusion of b does not verify against the size-3 root: %v", err)
	}

	cons, err := c.VlogProveConsistency(ctx, &vlogwire.ConsistencyRequest{Log: "ledger", Size1: 1, Size2: 3})
	if err != nil || cons.Size1 != 1 || cons.Size2 != 3 || len(cons.Hashes) == 0 {
		t.Fatalf("VlogProveConsistency = %+v, %v", cons, err)
	}
	if err := servicetest.VerifyConsistency(cons.Size1, cons.Size2, cons.Hashes, head1.Root, head.Root); err != nil {
		t.Fatalf("VlogProveConsistency 1 -> 3 does not verify against the heads' roots: %v", err)
	}

	signed, err := c.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "ledger"})
	if err != nil || signed.Head.Size != 3 || signed.Head.KeyVersion != 1 || signed.Head.SignatureType != vlog.SignatureEd25519 {
		t.Fatalf("VlogSignHead = %+v, %v", signed, err)
	}
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, signed.Checkpoint, vlog.HeadKey{Version: created.Key.Version, PublicKey: created.Key.PublicKey})

	rotated, err := c.VlogRotateKey(ctx, &vlogwire.RotateKeyRequest{Log: "ledger", SigningKeyType: vlog.SignatureECDSAP256SHA256})
	if err != nil || rotated.Key.Version != 2 || len(rotated.Key.PublicKey) == 0 {
		t.Fatalf("VlogRotateKey = %+v, %v; want key version 2", rotated, err)
	}
	if _, err := c.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "ledger", Entries: [][]byte{[]byte("d")}}); err != nil {
		t.Fatalf("VlogAppend after rotate: %v", err)
	}
	resigned, err := c.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "ledger"})
	if err != nil || resigned.Head.KeyVersion != 2 || resigned.Head.SignatureType != vlog.SignatureECDSAP256SHA256 {
		t.Fatalf("VlogSignHead after rotate = %+v, %v", resigned, err)
	}
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, resigned.Checkpoint, vlog.HeadKey{Version: rotated.Key.Version, PublicKey: rotated.Key.PublicKey})

	expired, err := c.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "ledger", Cutoff: 2})
	if err != nil || expired.Expired != 2 {
		t.Fatalf("VlogExpireBodies = %+v, %v; want 2 expired", expired, err)
	}
	after, err := c.VlogGet(ctx, &vlogwire.GetRequest{Log: "ledger"})
	if err != nil || len(after.Entries) != 4 || !after.Entries[1].Expired || after.Entries[2].Expired || string(after.Entries[3].Body) != "d" {
		t.Fatalf("VlogGet after expiry = %+v, %v; want entries 0 and 1 expired", after, err)
	}

	// The state machine's refusals reach the caller as their typed vlog
	// errors, rebuilt from the structured refusal every wire carries
	// (go-qrdb#912).
	ledgerID := vlog.LogID{TenantID: created.TenantID, Database: created.Database, Log: "ledger"}
	_, err = c.VlogCreate(ctx, &vlogwire.CreateRequest{Log: "ledger", Origin: servicetest.LogOrigin})
	if exists := (*vlog.LogExistsError)(nil); !errors.As(err, &exists) || exists.ID != ledgerID {
		t.Fatalf("second VlogCreate: err = %v (%T), want *vlog.LogExistsError naming %+v", err, err, ledgerID)
	}
	_, err = c.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "ledger"})
	if exists := (*vlog.CheckpointExistsError)(nil); !errors.As(err, &exists) || exists.Size != resigned.Head.Size {
		t.Fatalf("second VlogSignHead at size %d: err = %v (%T), want *vlog.CheckpointExistsError at it", resigned.Head.Size, err, err)
	}
	if _, err := c.VlogCreate(ctx, &vlogwire.CreateRequest{Log: "plain", Origin: servicetest.LogOrigin}); err != nil {
		t.Fatalf("VlogCreate plain: %v", err)
	}
	if _, err := c.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "plain", Entries: [][]byte{[]byte("p")}}); err != nil {
		t.Fatalf("VlogAppend plain: %v", err)
	}
	plainID := vlog.LogID{TenantID: created.TenantID, Database: created.Database, Log: "plain"}
	_, err = c.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "plain", Cutoff: 1})
	if refused := (*vlog.ExpireNotAllowedError)(nil); !errors.As(err, &refused) || refused.ID != plainID {
		t.Fatalf("VlogExpireBodies on a log without expiry: err = %v (%T), want *vlog.ExpireNotAllowedError naming %+v", err, err, plainID)
	}

	_, missing := c.VlogHead(ctx, &vlogwire.LogRequest{Log: "absent"})
	if knf := (*transport.KeyNotFoundError)(nil); !errors.As(missing, &knf) {
		t.Fatalf("VlogHead of an absent log: err = %v (%T), want *transport.KeyNotFoundError", missing, missing)
	}
	// A write to an absent log is refused with the typed error a read of it
	// returns: the state machine catches it, not a pre-proposal check, and
	// the refusal must not reach the caller as a generic one.
	writes := map[string]func() error{
		"VlogAppend": func() error {
			_, err := c.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "absent", Entries: [][]byte{[]byte("x")}})
			return err
		},
		"VlogSignHead": func() error {
			_, err := c.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "absent"})
			return err
		},
		"VlogExpireBodies": func() error {
			_, err := c.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "absent", Cutoff: 1})
			return err
		},
	}
	for name, write := range writes {
		if err := write(); err == nil || reflect.TypeOf(err) != reflect.TypeOf(missing) {
			t.Fatalf("%s to an absent log: err = %v (%T), want %T like VlogHead's", name, err, err, missing)
		}
	}
}
