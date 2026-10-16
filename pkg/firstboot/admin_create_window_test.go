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
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The user store's key layout, as the window store below must see it: the
// primary record a create writes AFTER its username claim, and the admin's
// username index entry that claim writes.
const (
	userPrimaryKeyPrefix   = "__sys:users/id/"
	adminUsernameIndexKey  = "__sys:user_idx/username/" + tenantid.System + "/" + adminUsername
	adminClaimRefusalsSeen = 2
)

// adminCreateWindowStore holds a create open between its username claim and
// its primary record, the window a user store without one guarded create
// batch exposes to every concurrent reader.
//
// The first primary-record Put blocks, after closing held, until release is
// closed. Every refused claim of the admin username is counted, and reclaimed
// closes on the second: a refused create that is attempted AGAIN is a loser
// waiting out the window rather than failing on it.
type adminCreateWindowStore struct {
	*guardedMemoryKVStore

	held      chan struct{}
	release   chan struct{}
	reclaimed chan struct{}

	holdOnce    sync.Once
	reclaimOnce sync.Once
	refused     atomic.Int32
}

func newAdminCreateWindowStore() *adminCreateWindowStore {
	return &adminCreateWindowStore{
		guardedMemoryKVStore: newGuardedMemoryKVStore(),
		held:                 make(chan struct{}),
		release:              make(chan struct{}),
		reclaimed:            make(chan struct{}),
	}
}

// Put holds the first primary-record write until release is closed.
func (s *adminCreateWindowStore) Put(ctx context.Context, key string, value []byte) error {
	if strings.HasPrefix(key, userPrimaryKeyPrefix) {
		first := false
		s.holdOnce.Do(func() { first = true })
		if first {
			close(s.held)
			<-s.release
		}
	}
	return s.guardedMemoryKVStore.Put(ctx, key, value)
}

// PutIfAbsent counts refused admin-username claims and closes reclaimed on
// the second.
func (s *adminCreateWindowStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := s.guardedMemoryKVStore.PutIfAbsent(ctx, key, value)
	if err == nil && !stored && key == adminUsernameIndexKey && s.refused.Add(1) == adminClaimRefusalsSeen {
		s.reclaimOnce.Do(func() { close(s.reclaimed) })
	}
	return stored, err
}

// TestInitialize_LoserWaitsOutTheWinnersAdminCreate pins go-qrdb#668: two
// first boots racing for the admin over one real user store yield exactly one
// admin, bound to the one adopted certificate, and BOTH runs succeed. The
// loser finds the admin username claimed by a create that has not written its
// record yet; it waits that create out and converges on the winner's admin
// rather than failing with "username already exists".
//
// The window is forced, not hoped for: the winner is held between its claim
// and its record until the loser has been refused AND has attempted the
// create again. A loser that refuses the in-flight create returns instead,
// and the test fails on that return.
func TestInitialize_LoserWaitsOutTheWinnersAdminCreate(t *testing.T) {
	kv := newAdminCreateWindowStore()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(kv, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	tenantSvc, err := tenant.NewKVTenantService(kv, discardLogger())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	ca := newTestLeafCA(t)
	certPEM := ca.issueLeaf(t, 2)
	deps := Dependencies{
		Tenants: tenantSvc, Users: userSvc, Roles: &fakeRoles{}, Logger: discardLogger(),
		AdminCertPEM: certPEM, AdminCertPool: ca.pool,
		Binder: UserCertBinder{Users: userSvc},
	}
	ctx := context.Background()

	type outcome struct {
		res *Result
		err error
	}
	run := func(out chan<- outcome) {
		res, err := Initialize(ctx, deps)
		out <- outcome{res, err}
	}

	winner := make(chan outcome, 1)
	go run(winner)
	select {
	case <-kv.held:
	case o := <-winner:
		t.Fatalf("winner returned before writing the admin record: %v", o.err)
	}

	loser := make(chan outcome, 1)
	go run(loser)
	select {
	case <-kv.reclaimed:
	case o := <-loser:
		close(kv.release)
		<-winner
		t.Fatalf("loser returned while the winner's admin create was in flight (err = %v); it must wait the create out", o.err)
	}
	close(kv.release)

	w, l := <-winner, <-loser
	if w.err != nil {
		t.Fatalf("winner Initialize: %v", w.err)
	}
	if l.err != nil {
		t.Fatalf("loser Initialize: %v", l.err)
	}
	if !w.res.AdminCreated || l.res.AdminCreated {
		t.Fatalf("AdminCreated: winner = %v, loser = %v; want exactly the winner", w.res.AdminCreated, l.res.AdminCreated)
	}

	list, err := userSvc.ListUsers(ctx, &user.ListUsersRequest{TenantID: tenantid.System})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	admins := 0
	for _, u := range list.Users {
		if u.Username == adminUsername {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("admin accounts = %d, want exactly 1", admins)
	}
	admin, err := userSvc.GetUserByUsername(ctx, tenantid.System, adminUsername)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	thumbprint, err := spkiThumbprintFromCertPEM(certPEM)
	if err != nil {
		t.Fatalf("spkiThumbprintFromCertPEM: %v", err)
	}
	if len(admin.CertBindings) != 1 || admin.CertBindings[0].Thumbprint != thumbprint {
		t.Fatalf("admin CertBindings = %+v, want exactly the adopted certificate's %q", admin.CertBindings, thumbprint)
	}
}
