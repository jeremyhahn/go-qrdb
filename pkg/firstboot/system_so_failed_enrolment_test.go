package firstboot

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// csrFailingSigner refuses to sign one CSR and signs every other.
type csrFailingSigner struct {
	*soSigner
	csr []byte
	err error
}

func (s *csrFailingSigner) SignAdminCSR(ctx context.Context, tenantID, subject string, csrPEM []byte) ([]byte, error) {
	if bytes.Equal(csrPEM, s.csr) {
		return nil, s.err
	}
	return s.soSigner.SignAdminCSR(ctx, tenantID, subject, csrPEM)
}

// keyFailingBinder refuses to bind one key and binds every other.
type keyFailingBinder struct {
	*soBinder
	key string
	err error
}

func (b *keyFailingBinder) BindAdminCert(ctx context.Context, tenantID string, userID uint64, thumbprint string) error {
	if thumbprint == b.key {
		return b.err
	}
	return b.soBinder.BindAdminCert(ctx, tenantID, userID, thumbprint)
}

// TestInitialize_FailedSOEnrolmentGrantsNoAuthority: an enrolment that fails
// at its PIN, its certificate or its binding leaves no active SO account and
// no SO role added to an existing account; the retry that succeeds activates
// the SO.
func TestInitialize_FailedSOEnrolmentGrantsNoAuthority(t *testing.T) {
	soRoleID := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	boom := errors.New("refused")
	breakPIN := func(h *soHarness) { h.pins.pins[tenantid.System] = "someone else's" }
	fixPIN := func(h *soHarness) { delete(h.pins.pins, tenantid.System) }
	breakSigner := func(h *soHarness) {
		h.deps.Signer = &csrFailingSigner{soSigner: h.signer, csr: h.deps.SO.CSRPEM, err: boom}
	}
	fixSigner := func(h *soHarness) { h.deps.Signer = h.signer }
	breakBinder := func(h *soHarness) {
		key, err := spkiThumbprintOf(h.deps.SO.CSRPEM, nil)
		if err != nil {
			t.Fatal(err)
		}
		h.deps.Binder = &keyFailingBinder{soBinder: h.binder, key: key, err: boom}
	}
	fixBinder := func(h *soHarness) { h.deps.Binder = h.binder }

	for _, tc := range []struct {
		name     string
		existing bool // the SO is the standard-mode bootstrap admin
		breakIt  func(*soHarness)
		fix      func(*soHarness)
	}{
		{"new account, someone else's PIN", false, breakPIN, fixPIN},
		{"new account, signing failure", false, breakSigner, fixSigner},
		{"new account, binding failure", false, breakBinder, fixBinder},
		{"existing account, someone else's PIN", true, breakPIN, fixPIN},
		{"existing account, signing failure", true, breakSigner, fixSigner},
		{"existing account, binding failure", true, breakBinder, fixBinder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSOHarness(t, !tc.existing)
			if tc.existing {
				h.deps.SO.Username = adminUsername
			}
			tc.breakIt(h)
			if _, err := Initialize(context.Background(), h.deps); err == nil {
				t.Fatal("Initialize succeeded with a broken enrolment")
			}
			so := h.users.byName[h.deps.SO.Username]
			if tc.existing {
				if so == nil {
					t.Fatal("no bootstrap admin")
				}
				if slices.Contains(so.RoleIDs, soRoleID) {
					t.Fatalf("admin roles = %v after a failed enrolment, want no SO role", so.RoleIDs)
				}
			} else if so != nil && so.Lifecycle == user.LifecycleActive {
				t.Fatalf("SO account %q is %s after a failed enrolment, want it pending", so.Username, so.Lifecycle)
			}

			tc.fix(h)
			result, err := Initialize(context.Background(), h.deps)
			if err != nil {
				t.Fatalf("retried Initialize: %v", err)
			}
			so = h.users.byName[h.deps.SO.Username]
			if so == nil || so.Lifecycle != user.LifecycleActive || !slices.Contains(so.RoleIDs, soRoleID) || result.SOUserID != so.ID {
				t.Fatalf("SO after the retry = %+v (result %+v), want an active SO account", so, result)
			}
		})
	}
}
