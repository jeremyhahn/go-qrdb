package firstboot

import (
	"context"
	"errors"
	"testing"
)

// adminBindRefused refuses every binding but the SO's while fail is set: the
// admin binding, Initialize's last write, fails after the SO is bound.
type adminBindRefused struct {
	*soBinder
	soName string
	fail   bool
}

func (b *adminBindRefused) BindAdminCert(ctx context.Context, tenantID string, userID uint64, thumbprint string) error {
	if u := b.users.byID(userID); b.fail && u != nil && u.Username != b.soName {
		return errors.New("admin binding refused")
	}
	return b.soBinder.BindAdminCert(ctx, tenantID, userID, thumbprint)
}

// An admin-binding failure after the SO's CSR was signed and bound must not
// cost the embedder the SO's login certificate: the retry returns one for
// the same key, matching the binding the failed run committed, and does not
// bind the SO again.
func TestInitialize_SOCertReturnedOnRetryAfterAdminBindFailure(t *testing.T) {
	ctx := context.Background()
	h := newSOHarness(t, true)
	h.deps.CA = &fakeCA{cert: realCertPEM(t), key: []byte("KEY")}
	binder := &adminBindRefused{soBinder: h.binder, soName: h.deps.SO.Username, fail: true}
	h.deps.Binder = binder

	_, err := Initialize(ctx, h.deps)
	var bindErr *AdminCertBindError
	if !errors.As(err, &bindErr) {
		t.Fatalf("first Initialize: %v, want the admin binding failure", err)
	}
	so := h.users.byName[h.deps.SO.Username]
	if so == nil || len(so.CertBindings) != 1 {
		t.Fatalf("the failed run must have bound the SO first: %+v", so)
	}

	binder.fail = false
	result, err := Initialize(ctx, h.deps)
	if err != nil {
		t.Fatalf("retry Initialize: %v", err)
	}
	if len(result.SOCertPEM) == 0 {
		t.Fatal("the retry returned no SO certificate: the embedder holds the SO key with no login certificate")
	}
	thumb, err := spkiThumbprintFromCertPEM(result.SOCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if thumb != so.CertBindings[0].Thumbprint || result.SOCertThumbprint != thumb {
		t.Fatalf("returned SO certificate %q does not match the SO's binding %q", thumb, so.CertBindings[0].Thumbprint)
	}
	if h.binder.calls[so.ID] != 1 || len(so.CertBindings) != 1 {
		t.Fatalf("SO bound %d times, %d bindings; the retry must not rebind it", h.binder.calls[so.ID], len(so.CertBindings))
	}
}
