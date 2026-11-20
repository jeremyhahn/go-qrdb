package firstboot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// soStubUsers is a scripted user store for the SO enrolment's write
// helpers: UpdateUser returns the queued errors in order, then always (nil
// for success), and GetUserByUsername returns the record or getErr.
type soStubUsers struct {
	mu      sync.Mutex
	rec     user.User
	getErr  error
	queued  []error
	always  error
	updates []user.UpdateUserRequest
}

func (s *soStubUsers) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, errors.New("soStubUsers: CreateUser is not scripted")
}

func (s *soStubUsers) UpdateUser(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, *req)
	err := s.always
	if len(s.queued) > 0 {
		err, s.queued = s.queued[0], s.queued[1:]
	}
	if err != nil {
		return nil, err
	}
	rec := s.rec
	return &rec, nil
}

func (s *soStubUsers) GetUserByUsername(context.Context, string, string) (*user.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	rec := s.rec
	rec.RoleIDs = slices.Clone(s.rec.RoleIDs)
	return &rec, nil
}

func (s *soStubUsers) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.updates)
}

// soUpdaterOnly hides GetUserByUsername, so the helper has no resolver.
type soUpdaterOnly struct{ s *soStubUsers }

func (u soUpdaterOnly) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	return u.s.CreateUser(ctx, req)
}

func (u soUpdaterOnly) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	return u.s.UpdateUser(ctx, req)
}

// soCreatorOnly cannot update a user at all.
type soCreatorOnly struct{}

func (soCreatorOnly) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, errors.New("soCreatorOnly: CreateUser is not scripted")
}

func soTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func soLostRace(id uint64) error { return &user.ConcurrentUpdateError{ID: id} }

// wantSOHelperErr checks err against the helper's documented outcome: nil,
// a MissingSOSeamError, or a SystemSOEnrollmentError with op.
func wantSOHelperErr(t *testing.T, err error, wantSeam bool, op string) {
	t.Helper()
	switch {
	case wantSeam:
		var seam *MissingSOSeamError
		if !errors.As(err, &seam) {
			t.Fatalf("err = %v, want a MissingSOSeamError", err)
		}
	case op != "":
		var enroll *SystemSOEnrollmentError
		if !errors.As(err, &enroll) || enroll.Op != op {
			t.Fatalf("err = %v, want a %q SystemSOEnrollmentError", err, op)
		}
	default:
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	}
}

func TestRevokeAdminSORole(t *testing.T) {
	soRoleID := role.SystemRoleID("system", role.RoleNameSO)
	admin := user.User{ID: 3, TenantID: "system", Username: "admin", RoleIDs: []string{"r-admin", soRoleID}}
	tests := []struct {
		name        string
		stub        soStubUsers
		noResolver  bool
		noUpdater   bool
		cancelled   bool
		wantSeam    bool
		wantOp      string
		wantUpdates int
	}{
		{name: "no updater is a missing seam", noUpdater: true, wantSeam: true},
		{name: "re-read failure", stub: soStubUsers{getErr: errors.New("store down")}, wantOp: "role rollback"},
		{name: "re-read finds another account", stub: soStubUsers{rec: user.User{ID: 9, TenantID: "system", Username: "admin"}}, wantOp: "role rollback"},
		{name: "role already gone", stub: soStubUsers{rec: user.User{ID: 3, TenantID: "system", Username: "admin", RoleIDs: []string{"r-admin"}}}},
		{name: "role taken back", stub: soStubUsers{rec: admin}, wantUpdates: 1},
		{name: "role taken back without a resolver", stub: soStubUsers{rec: admin}, noResolver: true, wantUpdates: 1},
		{name: "other update error is not retried", stub: soStubUsers{rec: admin, always: errors.New("disk full")}, wantOp: "role rollback", wantUpdates: 1},
		{name: "lost race is retried", stub: soStubUsers{rec: admin, queued: []error{soLostRace(3)}}, wantUpdates: 2},
		{name: "lost race past the window", stub: soStubUsers{rec: admin, always: soLostRace(3)}, wantOp: "role rollback", wantUpdates: -1},
		{name: "lost race on a cancelled context", stub: soStubUsers{rec: admin, always: soLostRace(3)}, cancelled: true, wantOp: "role rollback", wantUpdates: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cancelled {
				shrinkAdminConvergeWindow(t, time.Minute, time.Minute)
			} else {
				shrinkAdminConvergeWindow(t, 50*time.Millisecond, time.Millisecond)
			}
			stub := &tc.stub
			deps := Dependencies{Users: stub, Logger: soTestLogger()}
			switch {
			case tc.noUpdater:
				deps.Users = soCreatorOnly{}
			case tc.noResolver:
				deps.Users = soUpdaterOnly{s: stub}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			stale := admin
			err := revokeAdminSORole(ctx, deps, &stale, soRoleID)
			wantSOHelperErr(t, err, tc.wantSeam, tc.wantOp)
			got := stub.updateCount()
			switch {
			case tc.wantUpdates < 0:
				if got < 2 {
					t.Fatalf("updates = %d, want the lost race retried until the window closed", got)
				}
			case got != tc.wantUpdates:
				t.Fatalf("updates = %d, want %d", got, tc.wantUpdates)
			}
			if got > 0 && slices.Contains(stub.updates[got-1].RoleIDs, soRoleID) {
				t.Fatalf("rollback kept the SO role: %v", stub.updates[got-1].RoleIDs)
			}
		})
	}
}

func TestActivateSystemSO(t *testing.T) {
	pending := user.User{ID: 5, TenantID: "system", Username: "so", Lifecycle: user.LifecyclePending}
	tests := []struct {
		name        string
		so          user.User
		stub        soStubUsers
		noUpdater   bool
		cancelled   bool
		wantSeam    bool
		wantOp      string
		wantUpdates int
	}{
		{name: "an active SO is left alone", so: user.User{ID: 5, Lifecycle: user.LifecycleActive}, noUpdater: true},
		{name: "no updater is a missing seam", so: pending, noUpdater: true, wantSeam: true},
		{name: "activated", so: pending, wantUpdates: 1},
		{name: "other update error is not retried", so: pending, stub: soStubUsers{always: errors.New("disk full")}, wantOp: "activation", wantUpdates: 1},
		{name: "lost race is retried", so: pending, stub: soStubUsers{queued: []error{soLostRace(5)}}, wantUpdates: 2},
		{name: "lost race past the window", so: pending, stub: soStubUsers{always: soLostRace(5)}, wantOp: "activation", wantUpdates: -1},
		{name: "lost race on a cancelled context", so: pending, stub: soStubUsers{always: soLostRace(5)}, cancelled: true, wantOp: "activation", wantUpdates: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cancelled {
				shrinkAdminConvergeWindow(t, time.Minute, time.Minute)
			} else {
				shrinkAdminConvergeWindow(t, 50*time.Millisecond, time.Millisecond)
			}
			stub := &tc.stub
			deps := Dependencies{Users: stub, Logger: soTestLogger()}
			if tc.noUpdater {
				deps.Users = soCreatorOnly{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			so := tc.so
			err := activateSystemSO(ctx, deps, &so)
			wantSOHelperErr(t, err, tc.wantSeam, tc.wantOp)
			got := stub.updateCount()
			switch {
			case tc.wantUpdates < 0:
				if got < 2 {
					t.Fatalf("updates = %d, want the lost race retried until the window closed", got)
				}
			case got != tc.wantUpdates:
				t.Fatalf("updates = %d, want %d", got, tc.wantUpdates)
			}
			for _, req := range stub.updates {
				if req.Lifecycle != user.LifecycleActive {
					t.Fatalf("update lifecycle = %v, want active", req.Lifecycle)
				}
			}
		})
	}
}

func TestRecordSOBoundKey_FailurePaths(t *testing.T) {
	so := user.User{ID: 7, TenantID: "system", Username: "so"}
	tests := []struct {
		name      string
		stub      soStubUsers
		noUpdater bool
		cancelled bool
		wantSeam  bool
	}{
		{name: "no updater is a missing seam", noUpdater: true, wantSeam: true},
		{name: "lost race and the re-read fails", stub: soStubUsers{getErr: errors.New("store down"), always: soLostRace(7)}},
		{name: "lost race and the re-read finds another account", stub: soStubUsers{rec: user.User{ID: 8, TenantID: "system", Username: "so"}, always: soLostRace(7)}},
		{name: "lost race past the window", stub: soStubUsers{rec: so, always: soLostRace(7)}},
		{name: "lost race on a cancelled context", stub: soStubUsers{rec: so, always: soLostRace(7)}, cancelled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cancelled {
				shrinkAdminConvergeWindow(t, time.Minute, time.Minute)
			} else {
				shrinkAdminConvergeWindow(t, 50*time.Millisecond, time.Millisecond)
			}
			stub := &tc.stub
			deps := Dependencies{Users: stub, Logger: soTestLogger()}
			if tc.noUpdater {
				deps.Users = soCreatorOnly{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			stale := so
			err := recordSOBoundKey(ctx, deps, &stale, "key-a")
			op := "binding"
			if tc.wantSeam {
				op = ""
			}
			wantSOHelperErr(t, err, tc.wantSeam, op)
		})
	}
}

// updateFailingSOUsers refuses the UpdateUser calls fail matches and passes
// every other call, and every read, to the harness's store.
type updateFailingSOUsers struct {
	*soUsers
	fail func(*user.UpdateUserRequest) bool
}

func (u *updateFailingSOUsers) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if u.fail(req) {
		return nil, errors.New("update refused")
	}
	return u.soUsers.UpdateUser(ctx, req)
}

// enrolmentOps lists the Op of every SystemSOEnrollmentError joined in err.
func enrolmentOps(err error) []string {
	var ops []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if enroll, ok := e.(*SystemSOEnrollmentError); ok {
			ops = append(ops, enroll.Op)
		}
		switch w := e.(type) {
		case interface{ Unwrap() []error }:
			for _, c := range w.Unwrap() {
				walk(c)
			}
		case interface{ Unwrap() error }:
			walk(w.Unwrap())
		}
	}
	walk(err)
	return ops
}

func TestInitialize_SOEnrolmentWriteFailures(t *testing.T) {
	soRoleID := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	grantsSO := func(r *user.UpdateUserRequest) bool { return slices.Contains(r.RoleIDs, soRoleID) }
	revokesSO := func(r *user.UpdateUserRequest) bool { return r.RoleIDs != nil && !slices.Contains(r.RoleIDs, soRoleID) }
	records := func(r *user.UpdateUserRequest) bool { return r.Attributes != nil }
	activates := func(r *user.UpdateUserRequest) bool { return r.Lifecycle == user.LifecycleActive }
	soOf := func(h *soHarness) *user.User { return h.users.byName[h.deps.SO.Username] }

	for _, tc := range []struct {
		name string
		// existing makes the SO the standard-mode bootstrap admin.
		existing bool
		// bound runs a clean enrolment first, then prepare.
		bound   bool
		prepare func(*soHarness)
		fail    func(*user.UpdateUserRequest) bool
		wantOps []string
		check   func(*testing.T, *soHarness)
	}{
		{
			name: "new account, recording the bound key fails", fail: records, wantOps: []string{"binding"},
			check: func(t *testing.T, h *soHarness) {
				if so := soOf(h); so == nil || so.Lifecycle == user.LifecycleActive {
					t.Fatalf("SO = %+v, want a pending account", so)
				}
			},
		},
		{
			name: "new account, activation fails", fail: activates, wantOps: []string{"activation"},
			check: func(t *testing.T, h *soHarness) {
				if so := soOf(h); so == nil || so.Lifecycle == user.LifecycleActive {
					t.Fatalf("SO = %+v, want a pending account", so)
				}
			},
		},
		{
			name: "existing account, the role grant fails", existing: true, fail: grantsSO,
			check: func(t *testing.T, h *soHarness) {
				if so := soOf(h); so == nil || slices.Contains(so.RoleIDs, soRoleID) {
					t.Fatalf("admin = %+v, want no SO role", so)
				}
			},
		},
		{
			name: "existing account, recording fails and so does the rollback", existing: true,
			fail:    func(r *user.UpdateUserRequest) bool { return records(r) || revokesSO(r) },
			wantOps: []string{"binding", "role rollback"},
		},
		{
			name: "bound SO, recording the bound key fails", bound: true, fail: records, wantOps: []string{"binding"},
			prepare: func(h *soHarness) { delete(soOf(h).Attributes, soBoundKeyAttribute) },
		},
		{
			name: "bound SO, activation fails", bound: true, fail: activates, wantOps: []string{"activation"},
			prepare: func(h *soHarness) { soOf(h).Lifecycle = user.LifecyclePending },
		},
		{
			name: "bound SO, re-signing its CSR fails", bound: true, wantOps: []string{"certificate"},
			prepare: func(h *soHarness) {
				h.deps.Signer = &csrFailingSigner{soSigner: h.signer, csr: h.deps.SO.CSRPEM, err: errors.New("refused")}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSOHarness(t, !tc.existing)
			if tc.existing {
				h.deps.SO.Username = adminUsername
			}
			if tc.bound {
				if _, err := Initialize(context.Background(), h.deps); err != nil {
					t.Fatalf("first Initialize: %v", err)
				}
			}
			if tc.prepare != nil {
				tc.prepare(h)
			}
			if tc.fail != nil {
				h.deps.Users = &updateFailingSOUsers{soUsers: h.users, fail: tc.fail}
			}
			_, err := Initialize(context.Background(), h.deps)
			if err == nil {
				t.Fatal("Initialize succeeded with a failing enrolment write")
			}
			ops := enrolmentOps(err)
			for _, op := range tc.wantOps {
				if !slices.Contains(ops, op) {
					t.Fatalf("err = %v (ops %v), want a %q SystemSOEnrollmentError", err, ops, op)
				}
			}
			if tc.check != nil {
				tc.check(t, h)
			}
		})
	}
}
