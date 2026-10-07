// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// dao_sdk_test.go drives pkg/dao's GenericDAO -- the facade upstream
// projects (go-xkms, go-quicbaas, go-trusted-platform) build entity
// storage on -- through the exported sdk/go surface (sdk.NewDAO,
// sdk.DAOWithDatabase, sdk.NewDAOUnitOfWork) against the REAL qrdbd
// binary over its REST transport, exactly the way an embedding
// application reaches it: sdk.NewClient over a network transport, then
// client.KVStore() handed to the DAO. pkg/dao has no CLI verbs of its
// own -- it is a Go-only facade over the kv/index primitives the CLI
// already exercises per-protocol in kv_cli_test.go and index_cli_test.go
// -- so this file is the "uppermost abstraction" test CLAUDE.md asks for
// when a package has no CLI surface.
package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

// daoSDKUser exercises every index kind the docs promise (architecture.md
// "Index kinds"): Email is unique, Status is a plain equality index,
// Username is unique+case-insensitive, Nickname is sparse (omitempty, so a
// record that never sets it really omits the field), and Age is numeric --
// deliberately omitted from the planner's RANGE catalog per #35, so its
// equality lookup still indexes while a range query on it must fall back
// to a correct full scan rather than an incomplete one.
type daoSDKUser struct {
	ID       uint64 `json:"id"`
	Email    string `json:"email"              index:"unique"`
	Status   string `json:"status"             index:"true"`
	Username string `json:"username"           index:"unique,case_insensitive"`
	Nickname string `json:"nickname,omitempty" index:"true,sparse"`
	Age      int    `json:"age"                index:"true"`
	SortKey  string `json:"sort_key"`
	Payload  []byte `json:"payload,omitempty"`
}

func (u *daoSDKUser) EntityID() uint64      { return u.ID }
func (u *daoSDKUser) SetEntityID(id uint64) { u.ID = id }

// daoSDKClient dials the real qrdbd admin REST endpoint over mTLS with the
// harness's shared admin identity (the same certificate every node in this
// package binds during its ceremony) and returns a ready sdk.Client -- the
// exact construction an embedding application performs.
func daoSDKClient(t *testing.T, ca *testCA, port int) *sdk.Client {
	t.Helper()
	_, certPath, keyPath := ca.adminIdentity(t, t.TempDir())
	tr, err := rest.New(
		transport.WithAddress(fmt.Sprintf("https://127.0.0.1:%d", port)),
		transport.WithMTLS(certPath, keyPath, ca.CertPath),
		transport.WithTimeout(20*time.Second),
	)
	if err != nil {
		t.Fatalf("rest.New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("transport Connect to 127.0.0.1:%d: %v", port, err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	return client
}

// TestDAO_RealBinary_StorageBackends proves entity CRUD, every index kind
// and the documented #35 numeric-range fallback over a GenericDAO backed
// by every storage backend a DAO can sit on today: memory, pebble and
// file, selected the same way sm_storage_file_test.go selects them
// (--sm memory|pebble, --sm disk --sm-storage file), never a node-local
// shortcut.
func TestDAO_RealBinary_StorageBackends(t *testing.T) {
	t.Run("pebble_indexed", func(t *testing.T) {
		port, ca := startSingleNode(t, "pebble")
		client := daoSDKClient(t, ca, port)
		exerciseDAOEntityStorage(t, client, "daosdk-pebble")
	})

	t.Run("mvcc_indexed", func(t *testing.T) {
		port, ca := startSingleNode(t, "mvcc")
		client := daoSDKClient(t, ca, port)
		exerciseDAOEntityStorage(t, client, "daosdk-mvcc")
	})

	// "memory" and the storage-generic "disk" family (pkg/statemachine)
	// declare NO index capability at all -- see pkg/statemachine's
	// newDefaultRegistry, which wires every one of them without an index
	// manager. sdk.NewDAO's capability probe must degrade to full scans
	// rather than fail construction; exerciseDAOEntityStorageWithoutIndexCapability
	// proves exactly that, not index/unique enforcement these backends do
	// not and should not claim.
	t.Run("memory_no_index_capability", func(t *testing.T) {
		port, ca := startSingleNode(t, "memory")
		client := daoSDKClient(t, ca, port)
		exerciseDAOEntityStorageWithoutIndexCapability(t, client, "daosdk-memory")
	})

	t.Run("file_no_index_capability", func(t *testing.T) {
		// --sm disk --sm-storage file refuses to run with the seal barrier
		// enabled (file storage cannot be wrapped in the barrier's at-rest
		// encryption), the same constraint sm_storage_file_test.go runs
		// under: --auth-enabled=false, no seal ceremony, no client certificate.
		ca, port := startFileBackendNodeNoAuth(t)
		client := daoSDKClientNoAuth(t, ca, port)
		exerciseDAOEntityStorageWithoutIndexCapability(t, client, "daosdk-file")
	})
}

// startFileBackendNodeNoAuth starts a standalone node over the storage-generic
// "disk" state machine with the file storage backend. File storage cannot be
// wrapped in the seal barrier's at-rest encryption, so the barrier -- and
// therefore qrdb's own authentication, which depends on it -- must be
// disabled, mirroring sm_storage_file_test.go's startSMFileNode.
func startFileBackendNodeNoAuth(t *testing.T) (*testCA, int) {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
		"--auth-enabled=false",
		"--enable-rest",
		"--sm", "disk",
		"--sm-storage", "file",
	}
	args = append(args, ca.tlsFlags(t)...)
	startNode(t, bin, args, "[dao-file] ")
	waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[0]}, 60*time.Second)
	warmupWrite(t, ca, adminPorts[0], 15*time.Second)
	return ca, adminPorts[0]
}

// daoSDKClientNoAuth dials the real qrdbd admin REST endpoint with only
// server-side TLS (no client certificate), for a node started with
// --auth-enabled=false.
func daoSDKClientNoAuth(t *testing.T, ca *testCA, port int) *sdk.Client {
	t.Helper()
	tr, err := rest.New(
		transport.WithAddress(fmt.Sprintf("https://127.0.0.1:%d", port)),
		transport.WithTLS("", "", ca.CertPath),
		transport.WithTimeout(20*time.Second),
	)
	if err != nil {
		t.Fatalf("rest.New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("transport Connect to 127.0.0.1:%d: %v", port, err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	return client
}

// exerciseDAOEntityStorageWithoutIndexCapability runs entity CRUD,
// pagination/ordering, optimistic concurrency, an atomic UnitOfWork batch
// and large/binary values against a DAO backed by a state machine that
// declares NO index capability. sdk.NewDAO must still succeed -- the
// capability refusal degrades construction to full scans rather than
// failing it -- and Find().Where() must still answer correctly without an
// index to route to.
func exerciseDAOEntityStorageWithoutIndexCapability(t *testing.T, client *sdk.Client, entityType string) {
	t.Helper()
	ctx := context.Background()
	store := client.KVStore()

	userDAO, err := sdk.NewDAO[*daoSDKUser](store, entityType, func() *daoSDKUser { return &daoSDKUser{} })
	if err != nil {
		t.Fatalf("sdk.NewDAO over a store with no index capability: %v", err)
	}

	alice := &daoSDKUser{Email: "alice@example.com", Status: "active", Username: "Alice", Age: 30}
	if err := userDAO.Save(ctx, alice); err != nil {
		t.Fatalf("Save(alice): %v", err)
	}
	got, err := userDAO.Get(ctx, alice.ID)
	if err != nil || got.Email != alice.Email {
		t.Fatalf("Get(alice.ID) = %+v, err=%v, want it to match the entity just saved", got, err)
	}
	if err := userDAO.Delete(ctx, alice); err != nil {
		t.Fatalf("Delete(alice): %v", err)
	}
	if _, err = userDAO.Get(ctx, alice.ID); !sdk.IsDAONotFound(err) {
		t.Fatalf("Get(alice.ID) after Delete: err=%v, want a DAONotFoundError", err)
	}

	// Find().Where() has no index to route to, but must still answer
	// correctly by full scan.
	for i := 0; i < 3; i++ {
		u := &daoSDKUser{
			Email:    fmt.Sprintf("noidx-%d@example.com", i),
			Status:   "shipped",
			Username: fmt.Sprintf("NoIdx%d", i),
			Age:      70 + i,
		}
		if err := userDAO.Save(ctx, u); err != nil {
			t.Fatalf("Save(noidx #%d): %v", i, err)
		}
	}
	shipped, err := userDAO.Find(ctx).Where("Status = ?", "shipped").Execute()
	if err != nil {
		t.Fatalf("Find().Where(Status = shipped).Execute() over a non-indexing store: %v", err)
	}
	if len(shipped) != 3 {
		t.Fatalf("Find().Where(Status = shipped) over a non-indexing store returned %d entities, want 3", len(shipped))
	}

	exerciseDAOPagination(t, store, entityType+"-page")
	exerciseDAOOptimisticConcurrency(t, userDAO)
	exerciseDAOUnitOfWork(t, store, userDAO)
	exerciseDAOLargeAndBinaryValues(t, userDAO)
}

// exerciseDAOEntityStorage runs entity CRUD, every index kind and query
// operator the docs promise, pagination/ordering, optimistic concurrency,
// an atomic UnitOfWork batch, and large/binary values against one
// GenericDAO[*daoSDKUser] built over store.
func exerciseDAOEntityStorage(t *testing.T, client *sdk.Client, entityType string) {
	t.Helper()
	ctx := context.Background()
	store := client.KVStore()

	userDAO, err := sdk.NewDAO[*daoSDKUser](store, entityType, func() *daoSDKUser { return &daoSDKUser{} })
	if err != nil {
		t.Fatalf("sdk.NewDAO: %v", err)
	}

	// --- entity CRUD -----------------------------------------------------
	alice := &daoSDKUser{Email: "alice@example.com", Status: "active", Username: "Alice", Age: 30, SortKey: "m"}
	if err := userDAO.Save(ctx, alice); err != nil {
		t.Fatalf("Save(alice): %v", err)
	}
	if alice.ID == 0 {
		t.Fatal("Save(alice): EntityID was not assigned")
	}

	got, err := userDAO.Get(ctx, alice.ID)
	if err != nil {
		t.Fatalf("Get(alice.ID): %v", err)
	}
	if got.Email != alice.Email || got.Username != alice.Username {
		t.Fatalf("Get(alice.ID) = %+v, want it to match the entity just saved", got)
	}

	alice.Status = "inactive"
	if err := userDAO.Save(ctx, alice); err != nil {
		t.Fatalf("Save(alice) update: %v", err)
	}
	if got, err = userDAO.Get(ctx, alice.ID); err != nil || got.Status != "inactive" {
		t.Fatalf("Get(alice.ID) after update = %+v, err=%v, want Status=inactive", got, err)
	}

	if err := userDAO.Delete(ctx, alice); err != nil {
		t.Fatalf("Delete(alice): %v", err)
	}
	if _, err = userDAO.Get(ctx, alice.ID); !sdk.IsDAONotFound(err) {
		t.Fatalf("Get(alice.ID) after Delete: err=%v, want a DAONotFoundError", err)
	}
	// Delete is idempotent.
	if err := userDAO.Delete(ctx, alice); err != nil {
		t.Fatalf("Delete(alice) again (idempotent): %v", err)
	}

	// --- unique index enforces the constraint on write --------------------
	bob := &daoSDKUser{Email: "shared@example.com", Status: "active", Username: "Bob", Age: 40, SortKey: "a"}
	if err := userDAO.Save(ctx, bob); err != nil {
		t.Fatalf("Save(bob): %v", err)
	}
	carol := &daoSDKUser{Email: "shared@example.com", Status: "active", Username: "Carol", Age: 41, SortKey: "b"}
	if err := userDAO.Save(ctx, carol); err == nil {
		t.Fatalf("Save(carol) with bob's Email succeeded, want a unique-constraint refusal")
	}
	if _, err := userDAO.QueryByIndex(ctx, "Email", "shared@example.com"); err != nil {
		t.Fatalf("QueryByIndex(Email, shared@example.com): %v", err)
	}

	// --- unique + case-insensitive: differently-cased values collide ------
	dave := &daoSDKUser{Email: "dave@example.com", Status: "active", Username: "BOB", Age: 22, SortKey: "c"}
	if err := userDAO.Save(ctx, dave); err == nil {
		t.Fatalf("Save(dave) with Username %q (case-fold of bob's %q) succeeded, want a unique-constraint refusal",
			dave.Username, bob.Username)
	}
	found, err := userDAO.QueryOneByIndex(ctx, "Username", "BOB")
	if err != nil {
		t.Fatalf("QueryOneByIndex(Username, BOB) over a case_insensitive index: %v", err)
	}
	if found.ID != bob.ID {
		t.Fatalf("QueryOneByIndex(Username, BOB) = entity %d, want bob (%d)", found.ID, bob.ID)
	}

	// --- sparse index: a record that never sets the field is not indexed,
	// and is not lumped together with one storing the empty string. -------
	withNick := &daoSDKUser{Email: "nick1@example.com", Status: "active", Username: "Nick1", Age: 50, Nickname: "Nicky", SortKey: "d"}
	withoutNick := &daoSDKUser{Email: "nick2@example.com", Status: "active", Username: "Nick2", Age: 51, SortKey: "e"}
	if err := userDAO.Save(ctx, withNick); err != nil {
		t.Fatalf("Save(withNick): %v", err)
	}
	if err := userDAO.Save(ctx, withoutNick); err != nil {
		t.Fatalf("Save(withoutNick): %v", err)
	}
	nickMatches, err := userDAO.QueryByIndex(ctx, "Nickname", "Nicky")
	if err != nil {
		t.Fatalf("QueryByIndex(Nickname, Nicky): %v", err)
	}
	if len(nickMatches) != 1 || nickMatches[0].ID != withNick.ID {
		t.Fatalf("QueryByIndex(Nickname, Nicky) = %v, want exactly withNick", nickMatches)
	}

	// --- equality index + planner: Find().Where() picks the index --------
	for i := 0; i < 3; i++ {
		u := &daoSDKUser{
			Email:    fmt.Sprintf("shipped-%d@example.com", i),
			Status:   "shipped",
			Username: fmt.Sprintf("Shipped%d", i),
			Age:      60 + i,
			SortKey:  fmt.Sprintf("s%d", i),
		}
		if err := userDAO.Save(ctx, u); err != nil {
			t.Fatalf("Save(shipped #%d): %v", i, err)
		}
	}
	shipped, err := userDAO.Find(ctx).Where("Status = ?", "shipped").Execute()
	if err != nil {
		t.Fatalf("Find().Where(Status = shipped).Execute(): %v", err)
	}
	if len(shipped) != 3 {
		t.Fatalf("Find().Where(Status = shipped) returned %d entities, want 3", len(shipped))
	}

	// --- prefix operator over a string index ------------------------------
	prefixed, err := userDAO.Find(ctx).Where("Status PREFIX ?", "ship").Execute()
	if err != nil {
		t.Fatalf("Find().Where(Status PREFIX ship).Execute(): %v", err)
	}
	if len(prefixed) != 3 {
		t.Fatalf("Find().Where(Status PREFIX ship) returned %d entities, want 3", len(prefixed))
	}

	// --- range operator over a string index -------------------------------
	ranged, err := userDAO.Find(ctx).Where("Status >= ?", "s").Execute()
	if err != nil {
		t.Fatalf("Find().Where(Status >= s).Execute(): %v", err)
	}
	if len(ranged) < 3 {
		t.Fatalf("Find().Where(Status >= s) returned %d entities, want at least the 3 shipped", len(ranged))
	}

	// --- #35: a numeric field's equality lookup still indexes, but a range
	// query on it falls back to a (correct) full scan rather than an index
	// scan that would miss decimal-text-misordered entries. -----------------
	ageMatches, err := userDAO.QueryByIndex(ctx, "Age", 60)
	if err != nil {
		t.Fatalf("QueryByIndex(Age, 60): %v", err)
	}
	if len(ageMatches) != 1 {
		t.Fatalf("QueryByIndex(Age, 60) = %v, want exactly one match", ageMatches)
	}
	byAgeRange, err := userDAO.Find(ctx).Where("Age >= ?", 60).Execute()
	if err != nil {
		t.Fatalf("Find().Where(Age >= 60).Execute(): %v", err)
	}
	wantAtLeast := 3 // the three "shipped-N" entities with Age 60,61,62
	if len(byAgeRange) < wantAtLeast {
		t.Fatalf("Find().Where(Age >= 60) returned %d entities, want at least %d (the full-scan fallback must still be correct)",
			len(byAgeRange), wantAtLeast)
	}

	exerciseDAOPagination(t, store, entityType+"-page")
	exerciseDAOOptimisticConcurrency(t, userDAO)
	exerciseDAOUnitOfWork(t, store, userDAO)
	exerciseDAOLargeAndBinaryValues(t, userDAO)
}

// exerciseDAOPagination pins PageQuery.SortField's documented contract --
// the whole entity set is sorted before paging, not just the page-sized
// slice that landed in storage-key order (the unit-level regression this
// mirrors is pkg/dao's TestPage_SortFieldGlobalOrder) -- against the real
// server, and that the default (no SortField) walk visits every entity
// exactly once across pages.
func exerciseDAOPagination(t *testing.T, store sdk.KVStore, entityType string) {
	t.Helper()
	ctx := context.Background()

	pageDAO, err := sdk.NewDAO[*daoSDKUser](store, entityType, func() *daoSDKUser { return &daoSDKUser{} })
	if err != nil {
		t.Fatalf("sdk.NewDAO (pagination): %v", err)
	}

	names := []string{"Mia", "Alice", "Zara", "Kevin", "Omar", "Beth", "Yara", "Carl", "Nina", "Drew"}
	for i, name := range names {
		u := &daoSDKUser{
			Email:    fmt.Sprintf("page-%d@example.com", i),
			Status:   "active",
			Username: fmt.Sprintf("Page%d", i),
			Age:      i,
			SortKey:  name,
		}
		if err := pageDAO.Save(ctx, u); err != nil {
			t.Fatalf("Save(page entity %d): %v", i, err)
		}
	}

	wantOrder := append([]string(nil), names...)
	for i := 0; i < len(wantOrder); i++ {
		for j := i + 1; j < len(wantOrder); j++ {
			if wantOrder[j] < wantOrder[i] {
				wantOrder[i], wantOrder[j] = wantOrder[j], wantOrder[i]
			}
		}
	}

	var gotOrder []string
	seen := map[uint64]bool{}
	for page := 1; page <= len(names)+1; page++ {
		result, err := pageDAO.Page(ctx, sdk.PageQuery{Page: page, PageSize: 4, SortField: "SortKey"})
		if err != nil {
			t.Fatalf("Page(page=%d, SortField=SortKey): %v", page, err)
		}
		for _, e := range result.Entities {
			if seen[e.ID] {
				t.Fatalf("entity %d returned more than once across sorted pages", e.ID)
			}
			seen[e.ID] = true
			gotOrder = append(gotOrder, e.SortKey)
		}
		if !result.HasMore {
			break
		}
	}
	if len(gotOrder) != len(names) {
		t.Fatalf("paging with SortField visited %d entities, want %d", len(gotOrder), len(names))
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("sorted page order = %v, want %v", gotOrder, wantOrder)
		}
	}

	// Default order (no SortField): every entity is visited exactly once.
	seen = map[uint64]bool{}
	total := 0
	for page := 1; page <= len(names)+1; page++ {
		result, err := pageDAO.Page(ctx, sdk.PageQuery{Page: page, PageSize: 3})
		if err != nil {
			t.Fatalf("Page(page=%d): %v", page, err)
		}
		for _, e := range result.Entities {
			if seen[e.ID] {
				t.Fatalf("entity %d returned more than once across default-order pages", e.ID)
			}
			seen[e.ID] = true
			total++
		}
		if !result.HasMore {
			break
		}
	}
	if total != len(names) {
		t.Fatalf("paging without SortField visited %d entities, want %d", total, len(names))
	}
}

// exerciseDAOOptimisticConcurrency proves SaveIfAbsent, CompareAndSwap and
// DeleteIfMatch over the real server: each is the one-shot guarded verb
// the docs promise, never a Get-then-write race.
func exerciseDAOOptimisticConcurrency(t *testing.T, userDAO sdk.GenericDAO[*daoSDKUser]) {
	t.Helper()
	ctx := context.Background()

	const fixedID = uint64(900001)
	first := &daoSDKUser{ID: fixedID, Email: "occ1@example.com", Status: "active", Username: "Occ1", Age: 1}
	stored, err := userDAO.SaveIfAbsent(ctx, first)
	if err != nil {
		t.Fatalf("SaveIfAbsent(first): %v", err)
	}
	if !stored {
		t.Fatal("SaveIfAbsent(first) on a fresh ID: stored=false, want true")
	}

	second := &daoSDKUser{ID: fixedID, Email: "occ2@example.com", Status: "active", Username: "Occ2", Age: 2}
	stored, err = userDAO.SaveIfAbsent(ctx, second)
	if err != nil {
		t.Fatalf("SaveIfAbsent(second) on the same ID: %v", err)
	}
	if stored {
		t.Fatal("SaveIfAbsent(second) on an already-occupied ID: stored=true, want false")
	}

	current, err := userDAO.Get(ctx, fixedID)
	if err != nil {
		t.Fatalf("Get(fixedID): %v", err)
	}
	if current.Email != first.Email {
		t.Fatalf("Get(fixedID).Email = %q, want %q (second's SaveIfAbsent must not have overwritten it)", current.Email, first.Email)
	}

	next := &daoSDKUser{ID: fixedID, Email: current.Email, Status: "verified", Username: current.Username, Age: current.Age}
	swapped, err := userDAO.CompareAndSwap(ctx, current, next)
	if err != nil {
		t.Fatalf("CompareAndSwap(current, next): %v", err)
	}
	if !swapped {
		t.Fatal("CompareAndSwap(current, next) against the value just read: swapped=false, want true")
	}

	// A CAS against a now-stale copy must lose.
	staleNext := &daoSDKUser{ID: fixedID, Email: current.Email, Status: "stale-write", Username: current.Username, Age: current.Age}
	swapped, err = userDAO.CompareAndSwap(ctx, current, staleNext)
	if err != nil {
		t.Fatalf("CompareAndSwap(stale current, staleNext): %v", err)
	}
	if swapped {
		t.Fatal("CompareAndSwap against a stale expected value: swapped=true, want false")
	}

	fresh, err := userDAO.Get(ctx, fixedID)
	if err != nil {
		t.Fatalf("Get(fixedID) after CAS: %v", err)
	}
	deleted, err := userDAO.DeleteIfMatch(ctx, current) // current is now stale
	if err != nil {
		t.Fatalf("DeleteIfMatch(stale current): %v", err)
	}
	if deleted {
		t.Fatal("DeleteIfMatch against a stale copy: deleted=true, want false")
	}
	deleted, err = userDAO.DeleteIfMatch(ctx, fresh)
	if err != nil {
		t.Fatalf("DeleteIfMatch(fresh): %v", err)
	}
	if !deleted {
		t.Fatal("DeleteIfMatch against the current value: deleted=false, want true")
	}
	if _, err = userDAO.Get(ctx, fixedID); !sdk.IsDAONotFound(err) {
		t.Fatalf("Get(fixedID) after DeleteIfMatch: err=%v, want a DAONotFoundError", err)
	}
}

// exerciseDAOUnitOfWork proves that a DAOUnitOfWork spanning two saves and
// a delete commits as ONE atomic batch against the real server.
func exerciseDAOUnitOfWork(t *testing.T, store sdk.KVStore, userDAO sdk.GenericDAO[*daoSDKUser]) {
	t.Helper()
	ctx := context.Background()

	toDelete := &daoSDKUser{Email: "uow-delete@example.com", Status: "active", Username: "UowDelete", Age: 5}
	if err := userDAO.Save(ctx, toDelete); err != nil {
		t.Fatalf("Save(toDelete) before UnitOfWork: %v", err)
	}

	uow := sdk.NewDAOUnitOfWork(store)
	newA := &daoSDKUser{Email: "uow-a@example.com", Status: "active", Username: "UowA", Age: 6}
	newB := &daoSDKUser{Email: "uow-b@example.com", Status: "active", Username: "UowB", Age: 7}
	if err := sdk.RegisterDAOSave(uow, userDAO, newA); err != nil {
		t.Fatalf("RegisterDAOSave(newA): %v", err)
	}
	if err := sdk.RegisterDAOSave(uow, userDAO, newB); err != nil {
		t.Fatalf("RegisterDAOSave(newB): %v", err)
	}
	if err := sdk.RegisterDAODelete(uow, userDAO, toDelete); err != nil {
		t.Fatalf("RegisterDAODelete(toDelete): %v", err)
	}
	if err := uow.Commit(ctx); err != nil {
		t.Fatalf("UnitOfWork.Commit: %v", err)
	}

	if newA.ID == 0 || newB.ID == 0 {
		t.Fatalf("UnitOfWork.Commit did not assign IDs: newA.ID=%d newB.ID=%d", newA.ID, newB.ID)
	}
	if _, err := userDAO.Get(ctx, newA.ID); err != nil {
		t.Fatalf("Get(newA.ID) after Commit: %v", err)
	}
	if _, err := userDAO.Get(ctx, newB.ID); err != nil {
		t.Fatalf("Get(newB.ID) after Commit: %v", err)
	}
	if _, err := userDAO.Get(ctx, toDelete.ID); !sdk.IsDAONotFound(err) {
		t.Fatalf("Get(toDelete.ID) after Commit: err=%v, want a DAONotFoundError", err)
	}
}

// exerciseDAOLargeAndBinaryValues round-trips an entity carrying a large,
// non-UTF-8 binary payload through Save/Get against the real server.
func exerciseDAOLargeAndBinaryValues(t *testing.T, userDAO sdk.GenericDAO[*daoSDKUser]) {
	t.Helper()
	ctx := context.Background()

	binaryPayload := make([]byte, 256)
	for i := range binaryPayload {
		binaryPayload[i] = byte(i)
	}
	largePayload := make([]byte, 256*1024)
	for i := range largePayload {
		largePayload[i] = byte(i % 251) // a non-repeating-looking pattern, not all zero
	}

	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{name: "binary-all-byte-values", payload: binaryPayload},
		{name: "large-256KiB", payload: largePayload},
	} {
		u := &daoSDKUser{
			Email:    tc.name + "@example.com",
			Status:   "active",
			Username: tc.name,
			Age:      99,
			Payload:  tc.payload,
		}
		if err := userDAO.Save(ctx, u); err != nil {
			t.Fatalf("Save(%s): %v", tc.name, err)
		}
		got, err := userDAO.Get(ctx, u.ID)
		if err != nil {
			t.Fatalf("Get(%s): %v", tc.name, err)
		}
		if !bytes.Equal(got.Payload, tc.payload) {
			t.Fatalf("Get(%s).Payload round-trip mismatch: got %d bytes, want %d bytes", tc.name, len(got.Payload), len(tc.payload))
		}
	}
}

// TestDAO_RealBinary_WithDatabaseTenantScoped proves that a DAO configured
// with sdk.DAOWithDatabase, built over a database-scoped Client, is
// isolated from the default database: the same Email and the same entity
// ID may exist independently in both, and the unique index in one database
// never blocks a write in the other.
func TestDAO_RealBinary_WithDatabaseTenantScoped(t *testing.T) {
	port, ca := startSingleNode(t, "pebble")
	client := daoSDKClient(t, ca, port)
	ctx := context.Background()

	const dbName = "daosdk_tenant"
	if _, err := client.Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name: dbName, Backend: "memory",
	}); err != nil {
		t.Fatalf("CreateDatabase(%s): %v", dbName, err)
	}
	t.Cleanup(func() { _ = client.Database().DropDatabase(context.Background(), dbName) })

	scopedClient, err := client.DatabaseScoped(dbName)
	if err != nil {
		t.Fatalf("DatabaseScoped(%s): %v", dbName, err)
	}

	defaultDAO, err := sdk.NewDAO[*daoSDKUser](client.KVStore(), "tenanttest", func() *daoSDKUser { return &daoSDKUser{} })
	if err != nil {
		t.Fatalf("sdk.NewDAO (default db): %v", err)
	}
	scopedDAO, err := sdk.NewDAO[*daoSDKUser](scopedClient.KVStore(), "tenanttest", func() *daoSDKUser { return &daoSDKUser{} },
		sdk.DAOWithDatabase(dbName))
	if err != nil {
		t.Fatalf("sdk.NewDAO (scoped db): %v", err)
	}

	const sharedEmail = "same-email@example.com"
	defaultUser := &daoSDKUser{ID: 1, Email: sharedEmail, Status: "active", Username: "DefaultUser", Age: 1}
	scopedUser := &daoSDKUser{ID: 1, Email: sharedEmail, Status: "active", Username: "ScopedUser", Age: 1}

	if err := defaultDAO.Save(ctx, defaultUser); err != nil {
		t.Fatalf("Save(defaultUser) in the default database: %v", err)
	}
	// The SAME email and the SAME entity ID in the scoped database must
	// not collide with the default database's unique index or data.
	if err := scopedDAO.Save(ctx, scopedUser); err != nil {
		t.Fatalf("Save(scopedUser) in database %s with the same Email/ID as the default database's entity: %v", dbName, err)
	}

	gotDefault, err := defaultDAO.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1) in the default database: %v", err)
	}
	if gotDefault.Username != "DefaultUser" {
		t.Fatalf("Get(1) in the default database = %+v, want Username=DefaultUser (tenant leak from %s)", gotDefault, dbName)
	}

	gotScoped, err := scopedDAO.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1) in database %s: %v", dbName, err)
	}
	if gotScoped.Username != "ScopedUser" {
		t.Fatalf("Get(1) in database %s = %+v, want Username=ScopedUser (tenant leak from the default database)", dbName, gotScoped)
	}

	matches, err := scopedDAO.QueryByIndex(ctx, "Email", sharedEmail)
	if err != nil {
		t.Fatalf("QueryByIndex(Email) in database %s: %v", dbName, err)
	}
	if len(matches) != 1 || matches[0].Username != "ScopedUser" {
		t.Fatalf("QueryByIndex(Email, %s) in database %s = %v, want exactly ScopedUser", sharedEmail, dbName, matches)
	}
}
