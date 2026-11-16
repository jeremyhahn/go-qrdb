package firstboot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ssUsers is a user.UserServicer double that pages through a fixed set of
// accounts the way the store does. Only ListUsers is implemented; any other
// method panics through the nil embedded interface.
type ssUsers struct {
	user.UserServicer
	users   []*user.User
	listErr error
}

func (s *ssUsers) ListUsers(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	size := req.PageSize
	if size <= 0 {
		size = len(s.users) + 1
	}
	page := req.Page
	if page < 1 {
		page = 1
	}
	start := (page - 1) * size
	if start > len(s.users) {
		start = len(s.users)
	}
	end := min(start+size, len(s.users))
	return &user.ListUsersResponse{
		Users:   s.users[start:end],
		Total:   len(s.users),
		HasMore: end < len(s.users),
	}, nil
}

// ssRoles is a role.NameResolver double: roles[tenant][name] resolves, a
// missing name yields *role.RoleNotFoundError like the store, errs[tenant]
// fails every lookup in that tenant, and nilSO returns (nil, nil) for SO.
type ssRoles struct {
	roles map[string]map[string]*role.Role
	errs  map[string]error
	nilSO bool
	calls map[string]int
}

func (r *ssRoles) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[tenantID+"/"+name]++
	if err := r.errs[tenantID]; err != nil {
		return nil, err
	}
	if r.nilSO && name == role.RoleNameSO {
		return nil, nil
	}
	if rl, ok := r.roles[tenantID][name]; ok {
		return rl, nil
	}
	return nil, &role.RoleNotFoundError{ID: name}
}

const (
	ssSOID       = "role-so"
	ssSysAdminID = "role-admin-sys"
	ssT1AdminID  = "role-admin-t1"
	ssT2AdminID  = "role-admin-t2"
)

func ssStandardRoles() map[string]map[string]*role.Role {
	return map[string]map[string]*role.Role{
		tenantid.System: {
			role.RoleNameSO:    {ID: ssSOID},
			role.RoleNameAdmin: {ID: ssSysAdminID},
		},
		"t1": {role.RoleNameAdmin: {ID: ssT1AdminID}},
		"t2": {role.RoleNameAdmin: {ID: ssT2AdminID}},
	}
}

func TestCheckStrictSeparationHolders(t *testing.T) {
	errBoom := errors.New("boom")
	soOnly := &user.User{ID: 1, Username: "so", TenantID: tenantid.System, RoleIDs: []string{ssSOID}}
	adminOnly := &user.User{ID: 2, Username: "admin", TenantID: tenantid.System, RoleIDs: []string{ssSysAdminID}}

	tests := []struct {
		name      string
		users     *ssUsers
		roles     *ssRoles
		wantErrIs error
		wantSubs  []string
		wantSOAdm *StrictSeparationSOAdminError
		wantCalls map[string]int
	}{
		{
			name:  "SO role not found passes",
			users: &ssUsers{users: []*user.User{soOnly}},
			roles: &ssRoles{roles: map[string]map[string]*role.Role{}},
		},
		{
			name:      "SO resolve error is wrapped",
			users:     &ssUsers{},
			roles:     &ssRoles{errs: map[string]error{tenantid.System: errBoom}},
			wantErrIs: errBoom,
			wantSubs:  []string{"resolve the system SO role"},
		},
		{
			name:  "nil SO role passes",
			users: &ssUsers{users: []*user.User{soOnly}},
			roles: &ssRoles{roles: ssStandardRoles(), nilSO: true},
		},
		{
			name:      "listing SO holders fails",
			users:     &ssUsers{listErr: errBoom},
			roles:     &ssRoles{roles: ssStandardRoles()},
			wantErrIs: errBoom,
			wantSubs:  []string{"list the system SO holders"},
		},
		{
			name: "Admin resolve error names the tenant",
			users: &ssUsers{users: []*user.User{{
				ID: 3, Username: "split", TenantID: tenantid.System, RoleIDs: []string{ssSOID},
				RoleBindings: []user.RoleBinding{{RoleID: "role-x", TenantID: "t1"}},
			}}},
			roles:     &ssRoles{roles: ssStandardRoles(), errs: map[string]error{"t1": errBoom}},
			wantErrIs: errBoom,
			wantSubs:  []string{`resolve the Admin role in tenant "t1"`},
		},
		{
			name: "SO with system Admin is refused",
			users: &ssUsers{users: []*user.User{adminOnly, {
				ID: 4, Username: "both", TenantID: tenantid.System, RoleIDs: []string{ssSOID, ssSysAdminID},
			}}},
			roles:     &ssRoles{roles: ssStandardRoles()},
			wantSOAdm: &StrictSeparationSOAdminError{UserID: 4, Username: "both", AdminTenantID: tenantid.System},
		},
		{
			name: "holders across tenants resolve each tenant once",
			users: &ssUsers{users: []*user.User{soOnly, {
				ID: 5, Username: "multi", TenantID: tenantid.System, RoleIDs: []string{ssSOID},
				RoleBindings: []user.RoleBinding{
					{RoleID: "role-x", TenantID: "t1"},
					{RoleID: "role-y", TenantID: "t1"},
					{RoleID: ssT2AdminID, TenantID: "t2"},
				},
			}}},
			roles:     &ssRoles{roles: ssStandardRoles()},
			wantSOAdm: &StrictSeparationSOAdminError{UserID: 5, Username: "multi", AdminTenantID: "t2"},
			wantCalls: map[string]int{
				tenantid.System + "/" + role.RoleNameSO:    1,
				tenantid.System + "/" + role.RoleNameAdmin: 2,
				"t1/" + role.RoleNameAdmin:                 1,
				"t2/" + role.RoleNameAdmin:                 1,
			},
		},
		{
			name: "clean holder set passes",
			users: &ssUsers{users: []*user.User{soOnly, adminOnly, {
				ID: 6, Username: "tenant-so", TenantID: tenantid.System, RoleIDs: []string{ssSOID},
				RoleBindings: []user.RoleBinding{{RoleID: "role-x", TenantID: "t1"}},
			}}},
			roles: &ssRoles{roles: ssStandardRoles()},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckStrictSeparationHolders(context.Background(), tc.users, tc.roles)
			switch {
			case tc.wantSOAdm != nil:
				var got *StrictSeparationSOAdminError
				if !errors.As(err, &got) {
					t.Fatalf("err = %v, want *StrictSeparationSOAdminError", err)
				}
				if *got != *tc.wantSOAdm {
					t.Fatalf("err = %+v, want %+v", *got, *tc.wantSOAdm)
				}
				msg := got.Error()
				for _, sub := range []string{
					"strict separation of duties",
					tc.wantSOAdm.Username,
					`in tenant "` + tc.wantSOAdm.AdminTenantID + `"`,
					"auth.separation_of_duties=false",
				} {
					if !strings.Contains(msg, sub) {
						t.Errorf("Error() = %q, missing %q", msg, sub)
					}
				}
			case tc.wantErrIs != nil:
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err = %v, want wrapping %v", err, tc.wantErrIs)
				}
				var soAdm *StrictSeparationSOAdminError
				if errors.As(err, &soAdm) {
					t.Fatalf("err = %v, must not be a SO/Admin refusal", err)
				}
				for _, sub := range tc.wantSubs {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("err = %q, missing %q", err, sub)
					}
				}
			default:
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
			}
			for k, want := range tc.wantCalls {
				if got := tc.roles.calls[k]; got != want {
					t.Errorf("GetRoleByName(%s) calls = %d, want %d", k, got, want)
				}
			}
		})
	}
}
