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

package grpc

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// holderPageSize is ListUsers' default page; the tests seed more holders
// than that so a strip that stopped at one page would leave some behind.
const holderPageSize = 100

// commitFaultKV is a real memory store whose CompareAndSwap fails once a set
// number of commits have landed, interrupting a delete partway.
type commitFaultKV struct {
	*kvstore.MemoryKVStore
	failAfter atomic.Int64 // < 0 disarms
	swaps     atomic.Int64
}

func (s *commitFaultKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if limit := s.failAfter.Load(); limit >= 0 && s.swaps.Load() >= limit {
		return false, errors.New("injected commit failure")
	}
	ok, err := s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
	if ok && err == nil {
		s.swaps.Add(1)
	}
	return ok, err
}

// startHolderServer serves real role and user stores over kv, composed the
// way a node composes them, and seeds n holders of a doomed and a kept role.
func startHolderServer(t *testing.T, kv kvstore.KVStore, n int) (client pb.QRDBServiceClient, users user.UserServicer, doomed, kept string) {
	t.Helper()
	ctx := context.Background()
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	users, err = user.NewStore(kv, gen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	holders, err := user.NewRoleRevoker(users)
	if err != nil {
		t.Fatalf("NewRoleRevoker: %v", err)
	}
	roles, err := role.NewStore(kv, role.WithHolderRevoker(holders))
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	ids := make([]string, 0, 2)
	for _, name := range []string{"doomed", "kept"} {
		r, err := roles.CreateRole(ctx, &role.CreateRoleRequest{
			Name: name, TenantID: "acme", Permissions: []permission.Permission{permission.KVGet},
		})
		if err != nil {
			t.Fatalf("CreateRole(%s): %v", name, err)
		}
		ids = append(ids, r.ID)
	}
	for i := range n {
		name := "holder" + strconv.Itoa(i)
		if _, err := users.CreateUser(ctx, &user.CreateUserRequest{
			Username: name, Email: name + "@example.com", TenantID: "acme", RoleIDs: ids,
		}); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roles), WithUserService(users))
	t.Cleanup(cleanup)
	return client, users, ids[0], ids[1]
}

// requireNoHolder pages through every account in acme and fails if one still
// holds doomed or lost kept.
func requireNoHolder(t *testing.T, users user.UserServicer, doomed, kept string, want int) {
	t.Helper()
	seen := 0
	for page := 1; ; page++ {
		resp, err := users.ListUsers(context.Background(), &user.ListUsersRequest{TenantID: "acme", Page: page})
		if err != nil {
			t.Fatalf("ListUsers page %d: %v", page, err)
		}
		for _, u := range resp.Users {
			seen++
			if slices.Contains(u.RoleIDs, doomed) {
				t.Fatalf("user %s still holds the deleted role: %v", u.Username, u.RoleIDs)
			}
			if !slices.Contains(u.RoleIDs, kept) {
				t.Fatalf("user %s lost a role that was not deleted: %v", u.Username, u.RoleIDs)
			}
		}
		if !resp.HasMore {
			break
		}
	}
	if seen != want {
		t.Fatalf("saw %d accounts, want %d", seen, want)
	}
}

func TestGRPC_DeleteRole_StripsEveryHolder(t *testing.T) {
	const n = holderPageSize + 25
	client, users, doomed, kept := startHolderServer(t, kvstore.NewMemoryKVStore(), n)

	if _, err := client.DeleteRole(context.Background(), &pb.DeleteRoleRequest{Id: doomed}); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	requireNoHolder(t, users, doomed, kept, n)

	_, err := client.GetRole(context.Background(), &pb.GetRoleRequest{Id: doomed})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("GetRole after delete = %v, want NotFound", err)
	}
}

func TestGRPC_DeleteRole_AnInterruptedDeleteIsFinishedByRetry(t *testing.T) {
	const n = holderPageSize + 25
	kv := &commitFaultKV{MemoryKVStore: kvstore.NewMemoryKVStore()}
	kv.failAfter.Store(-1)
	client, users, doomed, kept := startHolderServer(t, kv, n)

	kv.failAfter.Store(kv.swaps.Load() + n/2)
	_, err := client.DeleteRole(context.Background(), &pb.DeleteRoleRequest{Id: doomed})
	if err == nil {
		t.Fatal("an interrupted DeleteRole reported success")
	}
	// The store refused the write outright, so the class is the store
	// failure's own: HolderRevocationError carries no row and defers to it.
	if code := status.Code(err); code != codes.Internal {
		t.Fatalf("interrupted DeleteRole code = %s, want Internal: %v", code, err)
	}

	// Mid-delete the role is still there, marked, and cannot be granted.
	got, err := client.GetRole(context.Background(), &pb.GetRoleRequest{Id: doomed})
	if err != nil {
		t.Fatalf("GetRole mid-delete: %v", err)
	}
	if got.GetId() != doomed {
		t.Fatalf("GetRole mid-delete returned %q", got.GetId())
	}

	kv.failAfter.Store(-1)
	if _, err := client.DeleteRole(context.Background(), &pb.DeleteRoleRequest{Id: doomed}); err != nil {
		t.Fatalf("retried DeleteRole: %v", err)
	}
	requireNoHolder(t, users, doomed, kept, n)
}
