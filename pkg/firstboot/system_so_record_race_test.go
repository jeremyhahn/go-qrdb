package firstboot

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// committingSOUsers is soUsers with a peer's write committed at the start of
// the first UpdateUser -- after the caller read the snapshot it computed its
// replacement from, before the store's own read -- and with
// UpdateUserRequest.Guard judged as the store judges it: against the record
// as committed, before anything is written.
type committingSOUsers struct {
	*soUsers
	commit  func(*user.User)
	updates int
}

func (c *committingSOUsers) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	c.updates++
	existing := c.byID(req.ID)
	if existing == nil {
		return nil, errors.New("no such user")
	}
	if c.commit != nil {
		c.commit(existing)
		c.commit = nil
	}
	if req.Guard != nil {
		prior := *existing
		prior.RoleIDs = slices.Clone(existing.RoleIDs)
		prior.Attributes = maps.Clone(existing.Attributes)
		next := prior
		if req.RoleIDs != nil {
			next.RoleIDs = slices.Clone(req.RoleIDs)
		}
		if req.Attributes != nil {
			next.Attributes = maps.Clone(req.Attributes)
		}
		if err := req.Guard(&prior, &next); err != nil {
			return nil, err
		}
	}
	return c.soUsers.UpdateUser(ctx, req)
}

func (c *committingSOUsers) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	u, err := c.soUsers.GetUserByUsername(ctx, tenantID, username)
	if err == nil {
		u.RoleIDs = slices.Clone(u.RoleIDs)
		u.Attributes = maps.Clone(u.Attributes)
	}
	return u, err
}

func committingFixture(u *user.User, commit func(*user.User)) (*committingSOUsers, Dependencies) {
	users := &committingSOUsers{soUsers: newSOUsers(), commit: commit}
	users.byName[u.Username] = u
	return users, Dependencies{Users: users, UserResolver: users, Logger: discardLogger()}
}

func TestRevokeAdminSORole_KeepsARoleAssignedAfterTheSnapshot(t *testing.T) {
	soRole := "so"
	admin := &user.User{ID: 1, TenantID: "system", Username: "admin", RoleIDs: []string{"admin", soRole}}
	users, deps := committingFixture(admin, func(u *user.User) {
		u.RoleIDs = append(u.RoleIDs, "auditor")
	})
	snapshot := *admin
	snapshot.RoleIDs = slices.Clone(admin.RoleIDs)
	if err := revokeAdminSORole(context.Background(), deps, &snapshot, soRole, "so-key"); err != nil {
		t.Fatalf("revokeAdminSORole: %v", err)
	}
	got := users.byName["admin"].RoleIDs
	if slices.Contains(got, soRole) {
		t.Fatalf("roles = %v: the SO role was not taken back", got)
	}
	if !slices.Contains(got, "auditor") {
		t.Fatalf("roles = %v: the rollback removed a role assigned after its snapshot", got)
	}
}

func TestGrantAdminSORole_KeepsARevocationCommittedAfterTheSnapshot(t *testing.T) {
	soRole := "so"
	admin := &user.User{ID: 1, TenantID: "system", Username: "admin", RoleIDs: []string{"admin", "auditor"}}
	users, deps := committingFixture(admin, func(u *user.User) {
		u.RoleIDs = slices.DeleteFunc(u.RoleIDs, func(id string) bool { return id == "auditor" })
	})
	snapshot := *admin
	snapshot.RoleIDs = slices.Clone(admin.RoleIDs)
	got, granted, err := grantAdminSORole(context.Background(), deps, &snapshot, soRole)
	if err != nil {
		t.Fatalf("grantAdminSORole: %v", err)
	}
	if !granted || got == nil || !slices.Contains(got.RoleIDs, soRole) {
		t.Fatalf("granted = %v, account = %+v: want the SO role granted", granted, got)
	}
	if roles := users.byName["admin"].RoleIDs; slices.Contains(roles, "auditor") {
		t.Fatalf("roles = %v: the grant restored a role revoked after its snapshot", roles)
	}
}

func TestGrantAdminSORole_PeerGrantIsNotOurs(t *testing.T) {
	soRole := "so"
	admin := &user.User{ID: 1, TenantID: "system", Username: "admin", RoleIDs: []string{"admin"}}
	users, deps := committingFixture(admin, func(u *user.User) {
		u.RoleIDs = append(u.RoleIDs, soRole)
	})
	snapshot := *admin
	snapshot.RoleIDs = slices.Clone(admin.RoleIDs)
	got, granted, err := grantAdminSORole(context.Background(), deps, &snapshot, soRole)
	if err != nil {
		t.Fatalf("grantAdminSORole: %v", err)
	}
	if granted {
		t.Fatal("a role a peer granted is reported as this run's grant, which a failed enrolment would take back")
	}
	if got == nil || !slices.Contains(got.RoleIDs, soRole) {
		t.Fatalf("account = %+v: want the peer's grant", got)
	}
	if n := len(users.byName["admin"].RoleIDs); n != 2 {
		t.Fatalf("roles = %v: want the SO role once", users.byName["admin"].RoleIDs)
	}
}

func TestRecordSOBoundKey_KeepsAnAttributeChangedAfterTheSnapshot(t *testing.T) {
	so := &user.User{ID: 2, TenantID: "system", Username: "so", Attributes: map[string]string{"team": "ops"}}
	users, deps := committingFixture(so, func(u *user.User) {
		u.Attributes = map[string]string{"team": "security"}
	})
	snapshot := *so
	snapshot.Attributes = maps.Clone(so.Attributes)
	if err := recordSOBoundKey(context.Background(), deps, &snapshot, "key-a"); err != nil {
		t.Fatalf("recordSOBoundKey: %v", err)
	}
	attrs := users.byName["so"].Attributes
	if attrs[soBoundKeyAttribute] != "key-a" {
		t.Fatalf("attributes = %v: want the bound key recorded", attrs)
	}
	if attrs["team"] != "security" {
		t.Fatalf("attributes = %v: the record restored an attribute changed after its snapshot", attrs)
	}
}

// racingSOUsers is a user surface on which a cluster peer commits to the SO
// record between UpdateUser's read and its write for the first lostRaces
// calls. peerKey is what the peer records under soBoundKeyAttribute.
type racingSOUsers struct {
	rec       user.User
	lostRaces int
	peerKey   string
	updates   int
}

func (u *racingSOUsers) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, errors.New("not used")
}

func (u *racingSOUsers) UpdateUser(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	u.updates++
	if u.lostRaces > 0 {
		u.lostRaces--
		attrs := maps.Clone(u.rec.Attributes)
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs["peer"] = "wrote"
		if u.peerKey != "" {
			attrs[soBoundKeyAttribute] = u.peerKey
		}
		u.rec.Attributes = attrs
		return nil, &user.ConcurrentUpdateError{ID: req.ID}
	}
	u.rec.Attributes = maps.Clone(req.Attributes)
	out := u.rec
	return &out, nil
}

func (u *racingSOUsers) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	out := u.rec
	out.Attributes = maps.Clone(u.rec.Attributes)
	return &out, nil
}

func TestRecordSOBoundKey_RetriesALostRaceOnTheFreshRecord(t *testing.T) {
	users := &racingSOUsers{
		rec:       user.User{ID: 7, TenantID: "system", Username: "so"},
		lostRaces: 2,
	}
	deps := Dependencies{Users: users, UserResolver: users}
	stale := users.rec
	if err := recordSOBoundKey(context.Background(), deps, &stale, "key-a"); err != nil {
		t.Fatalf("recordSOBoundKey after lost races: %v", err)
	}
	if got := users.rec.Attributes[soBoundKeyAttribute]; got != "key-a" {
		t.Fatalf("bound key = %q, want key-a", got)
	}
	if users.rec.Attributes["peer"] != "wrote" {
		t.Fatalf("the retry dropped the peer's attribute: %v", users.rec.Attributes)
	}
	if users.updates != 3 {
		t.Fatalf("UpdateUser calls = %d, want 3", users.updates)
	}
}

func TestRecordSOBoundKey_PeerRecordedTheSameKey(t *testing.T) {
	users := &racingSOUsers{
		rec:       user.User{ID: 7, TenantID: "system", Username: "so"},
		lostRaces: 1,
		peerKey:   "key-a",
	}
	deps := Dependencies{Users: users, UserResolver: users}
	stale := users.rec
	if err := recordSOBoundKey(context.Background(), deps, &stale, "key-a"); err != nil {
		t.Fatalf("recordSOBoundKey: %v", err)
	}
	if users.updates != 1 {
		t.Fatalf("UpdateUser calls = %d, want 1: the peer's record already names the key", users.updates)
	}
}

func TestRecordSOBoundKey_OtherErrorIsNotRetried(t *testing.T) {
	users := &racingSOUsers{rec: user.User{ID: 7, TenantID: "system", Username: "so"}}
	deps := Dependencies{Users: &failingSOUpdater{racingSOUsers: users}, UserResolver: users}
	stale := users.rec
	err := recordSOBoundKey(context.Background(), deps, &stale, "key-a")
	var enroll *SystemSOEnrollmentError
	if !errors.As(err, &enroll) || enroll.Op != "binding" {
		t.Fatalf("err = %v, want a binding SystemSOEnrollmentError", err)
	}
	if users.updates != 1 {
		t.Fatalf("UpdateUser calls = %d, want 1: a fault that is not a lost race is not retried", users.updates)
	}
}

type failingSOUpdater struct{ *racingSOUsers }

func (f *failingSOUpdater) UpdateUser(context.Context, *user.UpdateUserRequest) (*user.User, error) {
	f.updates++
	return nil, errors.New("store down")
}
