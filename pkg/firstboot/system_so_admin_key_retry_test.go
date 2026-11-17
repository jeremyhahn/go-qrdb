package firstboot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// soBindRefused refuses the system SO's certificate binding while fail is
// set, passing every other binding (the bootstrap admin's) through.
type soBindRefused struct {
	*soBinder
	soName string
	fail   bool
}

func (b *soBindRefused) BindAdminCert(ctx context.Context, tenantID string, userID uint64, thumbprint string) error {
	if u := b.users.byID(userID); b.fail && u != nil && u.Username == b.soName {
		return errors.New("SO binding refused")
	}
	return b.soBinder.BindAdminCert(ctx, tenantID, userID, thumbprint)
}

// soPromotingUsers adds the systemPromoter seam the real user service has,
// so a retry resolves the existing admin and can re-bind it.
type soPromotingUsers struct {
	*soUsers
}

func (u soPromotingUsers) PromoteToSystemUser(_ context.Context, id uint64) (*user.User, error) {
	existing := u.byID(id)
	if existing == nil {
		return nil, errors.New("no such user")
	}
	existing.System = true
	return existing, nil
}

// An SO enrollment failure after the CA generated the admin credential must
// commit no admin binding: a retry then sees an unbound admin and issues a
// fresh credential whose private key it returns, instead of skipping
// generation and leaving the failed run's key the only copy.
func TestInitialize_SOFailureKeepsGeneratedAdminKeyRecoverable(t *testing.T) {
	ctx := context.Background()
	h := newSOHarness(t, true)
	h.deps.CA = &fakeCA{cert: realCertPEM(t), key: []byte("KEY")}
	binder := &soBindRefused{soBinder: h.binder, soName: h.deps.SO.Username, fail: true}
	h.deps.Binder = binder
	h.deps.Users = soPromotingUsers{h.users}

	_, err := Initialize(ctx, h.deps)
	var e *SystemSOEnrollmentError
	if !errors.As(err, &e) || e.Op != "binding" {
		t.Fatalf("Initialize = %v, want an SO binding failure", err)
	}
	admin := h.users.byName[adminUsername]
	if admin == nil {
		t.Fatal("the failed run created no admin account")
	}
	if verifyAdminBindings(admin, time.Now().UTC()) == nil {
		t.Fatal("the failed run committed an admin binding whose key it never returned")
	}

	binder.fail = false
	result, err := Initialize(ctx, h.deps)
	if err != nil {
		t.Fatalf("retry Initialize = %v", err)
	}
	if len(result.AdminKeyPEM) == 0 || len(result.AdminCertPEM) == 0 {
		t.Fatal("the retry returned no generated admin credential")
	}
	if !h.users.byName[adminUsername].HasCertBinding(result.AdminCertThumbprint) {
		t.Fatal("the retry's admin credential is not bound to the admin")
	}
}
