package firstboot

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

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
}

type failingSOUpdater struct{ *racingSOUsers }

func (f *failingSOUpdater) UpdateUser(context.Context, *user.UpdateUserRequest) (*user.User, error) {
	f.updates++
	return nil, errors.New("store down")
}
