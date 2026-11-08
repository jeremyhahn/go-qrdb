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

package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// accountKind is what tells one tenant system role's account apart on the
// shared enrollment path: the typed errors its failures are reported with.
type accountKind struct {
	createErr  func(error) error
	certErr    func(error) error
	pendingErr func(op string, cause error) error
}

// accountKinds maps each tenant system role an account is enrolled with at
// tenant create to its kind.
var accountKinds = map[string]accountKind{
	role.RoleNameAdmin: {
		createErr:  func(err error) error { return &TenantAdminUserCreationError{Cause: err} },
		certErr:    func(err error) error { return &TenantAdminCertError{Cause: err} },
		pendingErr: func(op string, err error) error { return &PendingTenantAdminError{Op: op, Cause: err} },
	},
	role.RoleNameSO: {
		createErr: func(err error) error { return &TenantSOEnrollmentError{Op: "account creation", Cause: err} },
		certErr:   func(err error) error { return &TenantSOEnrollmentError{Op: "certificate", Cause: err} },
		pendingErr: func(op string, err error) error {
			return &TenantSOEnrollmentError{Op: "pending record " + op, Cause: err}
		},
	},
}

// soPlan is a judged firstboot.SOConfig: the SO's name, PIN and credential plan.
type soPlan struct {
	username string
	pin      *secure.Secret
	plan     *adminPlan
	// isAdmin marks a standard-posture SO that is the first admin itself:
	// the admin's account takes the so role and keeps its one credential.
	isAdmin bool
}

// planSO judges cfg.SO before the tenant is created, and returns nil when
// the request has none. The SO is refused when it is given in part, when its
// credential would be refused for the reasons the admin's would, under strict
// separation of duties when it is the admin by name or by key, and outside it
// when it is the admin by one of name and key but not the other. An SO that
// is the admin by both outside strict separation is the admin itself. admin
// is the admin's plan, whose trust anchors the SO's certificate is verified
// against.
func planSO(cfg TenantProvisionConfig, deps Dependencies, mode AdminEnrollmentMode, adminUsername string, admin *adminPlan, pending bool) (*soPlan, error) {
	so := cfg.SO
	if so == nil {
		return nil, nil
	}
	if err := so.Validate(); err != nil {
		return nil, err
	}
	// Tenant create is handed the PIN itself; a hashed record is first
	// boot's hand-off alone.
	if so.PIN == nil {
		return nil, &firstboot.IncompleteSOError{Field: "so_pin"}
	}
	// The SO is told apart from the admin, or found to be it, by key as well
	// as by name, and a deferred admin has no key yet to compare.
	if mode != AdminEnrollCert {
		return nil, &ConflictingAdminEnrollmentError{}
	}
	sameName := strings.EqualFold(so.Username, adminUsername)
	if sameName && deps.SeparationOfDuties {
		return nil, &firstboot.SOMatchesAdminError{Field: "username"}
	}
	if deps.SOPINs == nil {
		return nil, &MissingProvisioningSeamError{Seam: "Dependencies.SOPINs to commit the tenant SO's PIN"}
	}
	if pending {
		if deps.PendingSOs == nil {
			return nil, &MissingProvisioningSeamError{Seam: "Dependencies.PendingSOs for a tenant whose identity records are behind its own barrier"}
		}
		// As for the pending admin: the name goes into a certificate and a
		// pending record with no user-store validation until the barrier
		// is initialized.
		if !validAdminUsername(so.Username) {
			return nil, &InvalidAdminUsernameError{Username: so.Username}
		}
	}
	p := &adminPlan{anchors: admin.anchors}
	acct := accountCredential{username: so.Username, csrPEM: so.CSRPEM, certPEM: so.CertPEM, chainPEM: so.ChainPEM}
	if err := p.planCredential(deps, cfg.TenantID, acct, pending); err != nil {
		var rejected *AdminCredentialRejectedError
		if errors.As(err, &rejected) {
			return nil, &SOCredentialRejectedError{Cause: rejected.Cause}
		}
		return nil, err
	}
	sameKey := p.thumbprint == admin.thumbprint
	switch {
	case sameKey && deps.SeparationOfDuties:
		return nil, &firstboot.SOMatchesAdminError{Field: "key"}
	case sameName && !sameKey:
		return nil, &SOPartlyMatchesAdminError{Field: "username"}
	case sameKey && !sameName:
		return nil, &SOPartlyMatchesAdminError{Field: "key"}
	}
	if sameName {
		return &soPlan{username: adminUsername, pin: so.PIN, plan: p, isAdmin: true}, nil
	}
	return &soPlan{username: so.Username, pin: so.PIN, plan: p}, nil
}

// createTenantAccount creates a tenant account holding the tenant system
// role roleName and the alsoRoles beside it (the so role, for an admin a
// standard-posture tenant names as its SO), and pushes its deletion onto undo. On a convergent
// (non-exclusive) re-run that finds the account already present it returns
// nil, and nothing is undone for it.
func createTenantAccount(ctx context.Context, deps Dependencies, cfg TenantProvisionConfig, username, roleName string, disabled bool, undo *undoStack, alsoRoles ...string) (*user.User, error) {
	roleID := role.SystemRoleID(cfg.TenantID, roleName)
	roleIDs := []string{roleID}
	for _, also := range alsoRoles {
		roleIDs = append(roleIDs, role.SystemRoleID(cfg.TenantID, also))
	}
	account, err := deps.Users.CreateUser(ctx, &user.CreateUserRequest{
		// A bootstrap account authenticates by certificate, not by an
		// email login key, so the omission is declared rather than
		// defaulted (#190).
		WithoutEmailLoginKey: true,
		Username:             username,
		TenantID:             cfg.TenantID,
		RoleIDs:              roleIDs,
		Pending:              disabled,
	})
	if err != nil {
		var exists *user.UsernameExistsError
		if cfg.Exclusive || !errors.As(err, &exists) {
			return nil, accountKinds[roleName].createErr(err)
		}
		deps.Logger.Debug("bootstrap: tenant account already exists",
			slog.String("tenant", cfg.TenantID),
			slog.String("role", roleName),
			slog.String("username", username))
		return nil, nil
	}
	deps.Logger.Info("bootstrap: tenant account created",
		slog.String("tenant", cfg.TenantID),
		slog.String("role", roleName),
		slog.String("username", username),
		slog.String("role_id", roleID))
	createdID := account.ID
	undo.push("delete tenant "+roleName, func(ctx context.Context) error {
		d, ok := deps.Users.(userDeleter)
		if !ok {
			return &MissingProvisioningSeamError{Seam: "Users.DeleteUser"}
		}
		return d.DeleteUser(ctx, createdID)
	})
	return account, nil
}

// certifyAccount issues plan's credential for account, pushes its
// revocation onto undo and binds it to the account.
func certifyAccount(ctx context.Context, deps Dependencies, tenantID string, plan *adminPlan, account *user.User, username, roleName string, undo *undoStack) (*adminCredential, error) {
	c, err := plan.issue(ctx, tenantID, username, roleName)
	if err != nil {
		return nil, err
	}
	pushRevoke(undo, plan.signer, c)
	if err := bindAdmin(ctx, deps, tenantID, account, c, roleName); err != nil {
		return nil, err
	}
	return c, nil
}

// recordPendingAccount issues plan's credential for the account username
// will hold roleName as, and records it pending in store until the
// tenant's barrier is up (CompletePendingTenantAdmin). The revocation and
// the record's deletion are pushed onto undo.
func recordPendingAccount(ctx context.Context, tenantID string, store tenantadmin.Store, plan *adminPlan, username, roleName string, undo *undoStack) (*adminCredential, error) {
	c, err := plan.issue(ctx, tenantID, username, roleName)
	if err != nil {
		return nil, err
	}
	pushRevoke(undo, plan.signer, c)
	recorded := pendingFrom(username, c)
	if err := store.Create(ctx, tenantID, recorded); err != nil {
		return nil, accountKinds[roleName].pendingErr("record", err)
	}
	undo.push("delete pending "+roleName, func(ctx context.Context) error {
		_, err := store.Delete(ctx, tenantID, recorded)
		return err
	})
	return c, nil
}

// enrollSO creates the tenant SO's account with the so role, certifies its
// key and commits its PIN, onto result. An account a convergent re-run finds
// present keeps its credential.
func enrollSO(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies, so *soPlan, undo *undoStack, result *TenantProvisionResult) error {
	if so.isAdmin {
		return enrollAdminAsSO(ctx, cfg, deps, so, result)
	}
	result.SOUsername = so.username
	account, err := createTenantAccount(ctx, deps, cfg, so.username, role.RoleNameSO, false, undo)
	if err != nil {
		return err
	}
	if account != nil {
		c, err := certifyAccount(ctx, deps, cfg.TenantID, so.plan, account, so.username, role.RoleNameSO, undo)
		if err != nil {
			return err
		}
		result.SOUserID = account.ID
		result.setSOCredential(c)
	}
	return commitSOPIN(ctx, cfg, deps, so.pin)
}

// enrollAdminAsSO commits the SO PIN for a standard-posture SO that is the
// first admin itself, whose account was created holding the so role beside
// the admin role (soRolesBesideAdmin). The SO on result is the admin's account
// and credential: one key, one account.
func enrollAdminAsSO(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies, so *soPlan, result *TenantProvisionResult) error {
	result.SOUsername = so.username
	result.SOUserID = result.AdminUserID
	result.SOCertPEM = result.AdminCertPEM
	result.SOCertSerial = result.AdminCertSerial
	result.SOCertThumbprint = result.AdminCertThumbprint
	return commitSOPIN(ctx, cfg, deps, so.pin)
}

// soRolesBesideAdmin is the roles the first admin's account holds beside the
// admin role: the so role when the tenant names the admin as its SO, none
// otherwise.
func soRolesBesideAdmin(so *soPlan) []string {
	if so != nil && so.isAdmin {
		return []string{role.RoleNameSO}
	}
	return nil
}

// grantSORole adds the tenant's so role to account, the pending admin a
// standard-posture tenant named as its SO, when the pending SO is completed.
// An account that holds it already is left as it is.
func grantSORole(ctx context.Context, deps Dependencies, tenantID string, account *user.User) error {
	soID := role.SystemRoleID(tenantID, role.RoleNameSO)
	if slices.Contains(account.RoleIDs, soID) {
		return nil
	}
	updater, ok := deps.Users.(userUpdater)
	if !ok {
		return &MissingProvisioningSeamError{Seam: "Users.UpdateUser to grant the admin the tenant SO role"}
	}
	roleIDs := append(slices.Clone(account.RoleIDs), soID)
	if _, err := updater.UpdateUser(ctx, &user.UpdateUserRequest{ID: account.ID, RoleIDs: roleIDs}); err != nil {
		return accountKinds[role.RoleNameSO].createErr(err)
	}
	return nil
}

// provisionPendingSO records the tenant SO pending behind the tenant's
// barrier in Dependencies.PendingSOs (the __sys:pending-so record), the way
// provisionPendingAdmin records the admin, and commits its PIN. A
// convergent re-run that finds the SO pending keeps the recorded credential.
func provisionPendingSO(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies, so *soPlan, undo *undoStack, result *TenantProvisionResult) error {
	existing, err := deps.PendingSOs.Get(ctx, cfg.TenantID)
	if err != nil {
		return accountKinds[role.RoleNameSO].pendingErr("read", err)
	}
	result.SOUsername = so.username
	result.SOAwaitsBarrierInit = true
	switch {
	case existing != nil && !cfg.Exclusive:
		result.SOUsername = existing.Username
		result.SOCertThumbprint = existing.Thumbprint
		result.SOCertSerial = existing.Serial
	case so.isAdmin:
		if err := recordAdminAsPendingSO(ctx, cfg.TenantID, deps, undo, result); err != nil {
			return err
		}
	default:
		c, err := recordPendingAccount(ctx, cfg.TenantID, deps.PendingSOs, so.plan, so.username, role.RoleNameSO, undo)
		if err != nil {
			return err
		}
		result.setSOCredential(c)
	}
	return commitSOPIN(ctx, cfg, deps, so.pin)
}

// recordAdminAsPendingSO records the tenant's pending admin as its pending SO
// too, for a standard-posture SO that is the admin itself: the same account
// name and the same credential, so completion grants the admin's account the
// so role rather than creating a second account.
func recordAdminAsPendingSO(ctx context.Context, tenantID string, deps Dependencies, undo *undoStack, result *TenantProvisionResult) error {
	admin, err := deps.PendingAdmins.Get(ctx, tenantID)
	if err != nil {
		return accountKinds[role.RoleNameAdmin].pendingErr("read", err)
	}
	if admin == nil {
		return &MissingProvisioningSeamError{Seam: "Dependencies.PendingAdmins holding the admin named as the tenant SO"}
	}
	recorded := *admin
	if err := deps.PendingSOs.Create(ctx, tenantID, &recorded); err != nil {
		return accountKinds[role.RoleNameSO].pendingErr("record", err)
	}
	undo.push("delete pending "+role.RoleNameSO, func(ctx context.Context) error {
		_, err := deps.PendingSOs.Delete(ctx, tenantID, &recorded)
		return err
	})
	result.SOUsername = recorded.Username
	result.SOCertPEM = result.AdminCertPEM
	result.SOCertSerial = recorded.Serial
	result.SOCertThumbprint = recorded.Thumbprint
	return nil
}

// commitSOPIN writes the tenant SO's PIN, the last step of the SO's
// enrollment: the PIN record has no undo, and an undone provisioning deletes
// the tenant, whose id is then retired and never reused. A convergent re-run
// finds it already committed and keeps it.
func commitSOPIN(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies, pin *secure.Secret) error {
	if err := firstboot.CommitSOPIN(ctx, deps.SOPINs, cfg.TenantID, pin); err != nil {
		if !cfg.Exclusive && errors.Is(err, auth.ErrSOPINAlreadySet) {
			return nil
		}
		return &TenantSOEnrollmentError{Op: "PIN commit", Cause: err}
	}
	return nil
}

// setSOCredential reports the SO's issued credential on r.
func (r *TenantProvisionResult) setSOCredential(c *adminCredential) {
	r.SOCertPEM = c.certPEM
	r.SOCertSerial = c.serial
	r.SOCertThumbprint = c.thumbprint
}
