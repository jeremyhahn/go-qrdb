package firstboot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// scriptedUsers is soUsers with scripted failures: each UpdateUser call
// consumes the next entry of updateErrs (a nil entry, or none left, lets
// the write through), and getErr fails every lookup.
type scriptedUsers struct {
	*soUsers
	updateErrs []error
	updates    int
	getErr     error
	// failUpdate, when set, refuses every write it returns an error for.
	failUpdate func(*user.UpdateUserRequest) error
}

func (s *scriptedUsers) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	s.updates++
	if s.failUpdate != nil {
		if err := s.failUpdate(req); err != nil {
			return nil, err
		}
	}
	if len(s.updateErrs) > 0 {
		err := s.updateErrs[0]
		s.updateErrs = s.updateErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return s.soUsers.UpdateUser(ctx, req)
}

func (s *scriptedUsers) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.soUsers.GetUserByUsername(ctx, tenantID, username)
}

// soCreatorOnly offers CreateUser alone: no UpdateUser, no lookup.
type soCreatorOnly struct{ UserCreator }

// updaterNoLookup offers CreateUser and UpdateUser but no lookup.
type updaterNoLookup struct {
	UserCreator
	up userUpdater
}

func (u updaterNoLookup) UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	return u.up.UpdateUser(ctx, req)
}

// soHelperFixture seeds one account and returns Dependencies over users.
func soHelperFixture(t *testing.T, u *user.User) (*soUsers, Dependencies) {
	t.Helper()
	users := newSOUsers()
	users.byName[u.Username] = u
	return users, Dependencies{Users: users, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// shrinkConvergeWindow sets adminConvergeWindow for one test.
func shrinkConvergeWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := adminConvergeWindow
	adminConvergeWindow = d
	t.Cleanup(func() { adminConvergeWindow = old })
}

func lostRace() error { return &user.ConcurrentUpdateError{} }

func TestSOHelpers_FailurePaths(t *testing.T) {
	soRoleID := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	boom := errors.New("refused")
	newUser := func() *user.User {
		return &user.User{
			ID:        7,
			TenantID:  tenantid.System,
			Username:  "so",
			RoleIDs:   []string{"other", soRoleID},
			Lifecycle: user.LifecyclePending,
		}
	}
	type wiring func(t *testing.T, s *soUsers, deps *Dependencies) *scriptedUsers
	scripted := func(updateErrs []error, getErr error) wiring {
		return func(_ *testing.T, s *soUsers, deps *Dependencies) *scriptedUsers {
			w := &scriptedUsers{soUsers: s, updateErrs: updateErrs, getErr: getErr}
			deps.Users = w
			return w
		}
	}
	noUpdater := func(_ *testing.T, s *soUsers, deps *Dependencies) *scriptedUsers {
		deps.Users = soCreatorOnly{s}
		return nil
	}
	noLookup := func(updateErrs []error) wiring {
		return func(_ *testing.T, s *soUsers, deps *Dependencies) *scriptedUsers {
			w := &scriptedUsers{soUsers: s, updateErrs: updateErrs}
			deps.Users = updaterNoLookup{UserCreator: s, up: w}
			return w
		}
	}
	revoke := func(ctx context.Context, deps Dependencies, u *user.User) error {
		return revokeAdminSORole(ctx, deps, u, soRoleID)
	}
	record := func(ctx context.Context, deps Dependencies, u *user.User) error {
		return recordSOBoundKey(ctx, deps, u, "key")
	}

	for _, tc := range []struct {
		name    string
		call    func(context.Context, Dependencies, *user.User) error
		wire    wiring
		prep    func(*user.User)
		cancel  bool
		window  time.Duration
		wantErr bool
		check   func(t *testing.T, s *soUsers)
	}{
		// revokeAdminSORole
		{name: "revoke: no updater", call: revoke, wire: noUpdater, wantErr: true},
		{name: "revoke: re-read fails", call: revoke, wire: scripted(nil, boom), wantErr: true},
		{name: "revoke: role already gone", call: revoke, wire: scripted(nil, nil),
			prep: func(u *user.User) { u.RoleIDs = []string{"other"} }},
		{name: "revoke: write refused", call: revoke, wire: scripted([]error{boom}, nil), wantErr: true},
		{name: "revoke: lost race retried", call: revoke, wire: scripted([]error{lostRace()}, nil),
			check: func(t *testing.T, s *soUsers) {
				if slices.Contains(s.byName["so"].RoleIDs, soRoleID) {
					t.Fatalf("roles = %v, want the SO role taken back", s.byName["so"].RoleIDs)
				}
			}},
		{name: "revoke: lost race, window spent", call: revoke, wire: scripted([]error{lostRace()}, nil),
			window: -time.Second, wantErr: true},
		{name: "revoke: lost race, cancelled", call: revoke, wire: scripted([]error{lostRace()}, nil),
			cancel: true, wantErr: true},
		{name: "revoke: no lookup uses the given record", call: revoke, wire: noLookup(nil)},

		// activateSystemSO
		{name: "activate: already active", call: activateSystemSO, wire: noUpdater,
			prep: func(u *user.User) { u.Lifecycle = user.LifecycleActive }},
		{name: "activate: no updater", call: activateSystemSO, wire: noUpdater, wantErr: true},
		{name: "activate: write refused", call: activateSystemSO, wire: scripted([]error{boom}, nil), wantErr: true},
		{name: "activate: lost race retried", call: activateSystemSO, wire: scripted([]error{lostRace()}, nil),
			check: func(t *testing.T, s *soUsers) {
				if got := s.byName["so"].Lifecycle; got != user.LifecycleActive {
					t.Fatalf("lifecycle = %s, want active", got)
				}
			}},
		{name: "activate: lost race, window spent", call: activateSystemSO,
			wire: scripted([]error{lostRace()}, nil), window: -time.Second, wantErr: true},
		{name: "activate: lost race, cancelled", call: activateSystemSO,
			wire: scripted([]error{lostRace()}, nil), cancel: true, wantErr: true},

		// recordSOBoundKey
		{name: "record: already recorded", call: record, wire: noUpdater,
			prep: func(u *user.User) { u.Attributes = map[string]string{soBoundKeyAttribute: "key"} }},
		{name: "record: no updater", call: record, wire: noUpdater, wantErr: true},
		{name: "record: write refused", call: record, wire: scripted([]error{boom}, nil), wantErr: true},
		{name: "record: lost race, no lookup", call: record, wire: noLookup([]error{lostRace()}), wantErr: true},
		{name: "record: lost race, window spent", call: record,
			wire: scripted([]error{lostRace()}, nil), window: -time.Second, wantErr: true},
		{name: "record: lost race, re-read fails", call: record,
			wire: scripted([]error{lostRace()}, boom), wantErr: true},
		{name: "record: lost race, peer recorded it", call: record,
			wire: func(t *testing.T, s *soUsers, deps *Dependencies) *scriptedUsers {
				s.byName["so"].Attributes = map[string]string{soBoundKeyAttribute: "key"}
				return scripted([]error{lostRace()}, nil)(t, s, deps)
			},
			prep: func(u *user.User) { u.Attributes = map[string]string{"a": "b"} }},
		{name: "record: lost race retried", call: record, wire: scripted([]error{lostRace()}, nil),
			check: func(t *testing.T, s *soUsers) {
				if got := s.byName["so"].Attributes[soBoundKeyAttribute]; got != "key" {
					t.Fatalf("bound key = %q, want recorded", got)
				}
			}},
		{name: "record: lost race, cancelled", call: record,
			wire: scripted([]error{lostRace()}, nil), cancel: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.window != 0 {
				shrinkConvergeWindow(t, tc.window)
			}
			stored := newUser()
			if tc.prep != nil {
				tc.prep(stored)
			}
			given := *stored
			given.RoleIDs = slices.Clone(stored.RoleIDs)
			s, deps := soHelperFixture(t, stored)
			// given shares stored's Attributes map until a peer's write
			// (a wiring) replaces it: the caller holds the older record.
			w := tc.wire(t, s, &deps)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := tc.call(ctx, deps, &given)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				var enrol *SystemSOEnrollmentError
				var seam *MissingSOSeamError
				if !errors.As(err, &enrol) && !errors.As(err, &seam) {
					t.Fatalf("err = %T %v, want a SystemSOEnrollmentError or MissingSOSeamError", err, err)
				}
			}
			if w != nil && len(w.updateErrs) != 0 {
				t.Fatalf("%d scripted UpdateUser failures never reached", len(w.updateErrs))
			}
			if tc.check != nil {
				tc.check(t, s)
			}
		})
	}
}

// TestInitialize_SOEnrolmentWriteFailures fails each account write the SO
// enrolment makes, on the run that binds the SO's key and on a retry that
// finds it already bound, and checks a clean retry still enrols the SO.
func TestInitialize_SOEnrolmentWriteFailures(t *testing.T) {
	soRoleID := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	boom := errors.New("refused")
	failGrant := func(req *user.UpdateUserRequest) error {
		if slices.Contains(req.RoleIDs, soRoleID) {
			return boom
		}
		return nil
	}
	failRevoke := func(req *user.UpdateUserRequest) error {
		if req.RoleIDs != nil && !slices.Contains(req.RoleIDs, soRoleID) {
			return boom
		}
		return nil
	}
	failRecord := func(req *user.UpdateUserRequest) error {
		if _, ok := req.Attributes[soBoundKeyAttribute]; ok {
			return boom
		}
		return nil
	}
	failActivate := func(req *user.UpdateUserRequest) error {
		if req.Lifecycle == user.LifecycleActive {
			return boom
		}
		return nil
	}
	type run struct {
		fail        func(*user.UpdateUserRequest) error
		breakSigner bool
		breakBinder bool
	}
	for _, tc := range []struct {
		name     string
		existing bool // the SO is the standard-mode bootstrap admin
		runs     []run
	}{
		{"new account, activation refused when bound and when rebound", false,
			[]run{{fail: failActivate}, {fail: failActivate}}},
		{"new account, key record refused when bound and when rebound", false,
			[]run{{fail: failRecord}, {fail: failRecord}}},
		{"new account, activation refused then re-signing refused", false,
			[]run{{fail: failActivate}, {breakSigner: true}}},
		{"existing account, grant refused", true,
			[]run{{fail: failGrant}}},
		{"existing account, binding and role rollback refused", true,
			[]run{{fail: failRevoke, breakBinder: true}}},
		{"existing account, key record refused then grant refused when rebound", true,
			[]run{{fail: failRecord}, {fail: failGrant}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSOHarness(t, !tc.existing)
			if tc.existing {
				h.deps.SO.Username = adminUsername
			}
			users := &scriptedUsers{soUsers: h.users}
			h.deps.Users = users
			key, err := spkiThumbprintOf(h.deps.SO.CSRPEM, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i, r := range tc.runs {
				users.failUpdate = r.fail
				h.deps.Signer, h.deps.Binder = h.signer, h.binder
				if r.breakSigner {
					h.deps.Signer = &csrFailingSigner{soSigner: h.signer, csr: h.deps.SO.CSRPEM, err: boom}
				}
				if r.breakBinder {
					h.deps.Binder = &keyFailingBinder{soBinder: h.binder, key: key, err: boom}
				}
				_, err := Initialize(context.Background(), h.deps)
				if !errors.Is(err, boom) {
					t.Fatalf("run %d: err = %v, want the refused write", i, err)
				}
				so := h.users.byName[h.deps.SO.Username]
				if so != nil && so.Lifecycle == user.LifecycleActive && !tc.existing {
					t.Fatalf("run %d: SO account active after a failed enrolment", i)
				}
			}

			users.failUpdate = nil
			h.deps.Signer, h.deps.Binder = h.signer, h.binder
			result, err := Initialize(context.Background(), h.deps)
			if err != nil {
				t.Fatalf("clean retry: %v", err)
			}
			so := h.users.byName[h.deps.SO.Username]
			if so == nil || so.Lifecycle != user.LifecycleActive || !slices.Contains(so.RoleIDs, soRoleID) || result.SOUserID != so.ID {
				t.Fatalf("SO after the retry = %+v (result %+v), want an active SO account", so, result)
			}
		})
	}
}
