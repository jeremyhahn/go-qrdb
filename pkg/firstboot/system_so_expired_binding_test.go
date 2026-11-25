package firstboot

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// An SO whose binding for its own key has expired, while another key's
// binding is still live, is not enrolled under its key: its certificate
// would fail User.MatchesCert. A re-run must renew the binding for the SO's
// key and return a certificate that authenticates, not report success over
// the expired binding.
func TestInitialize_SOExpiredBindingForItsKeyIsRenewed(t *testing.T) {
	ctx := context.Background()
	h := newSOHarness(t, true)
	if _, err := Initialize(ctx, h.deps); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}
	so := h.users.byName[h.deps.SO.Username]
	if so == nil || len(so.CertBindings) != 1 {
		t.Fatalf("the first run must bind the SO once: %+v", so)
	}
	soKey := so.CertBindings[0].Thumbprint

	// The SO's own key's binding expires; another key of the account stays
	// live, so the account as a whole still holds a live binding.
	so.CertBindings[0].NotAfter = time.Now().UTC().Add(-time.Hour)
	so.CertBindings = append(so.CertBindings, user.CertBinding{
		Thumbprint: "another-live-key", Kind: user.CertBindingSPKIThumbprint,
	})
	if so.HasCertBinding(soKey) {
		t.Fatal("setup: the SO key's binding must be expired")
	}

	result, err := Initialize(ctx, h.deps)
	if err != nil {
		t.Fatalf("re-run Initialize: %v", err)
	}
	if !so.HasCertBinding(soKey) {
		t.Fatal("the re-run reported success while the SO's key stays bound only by an expired binding")
	}
	if h.binder.calls[so.ID] != 2 {
		t.Fatalf("SO bound %d times, want the re-run to renew its key's binding", h.binder.calls[so.ID])
	}
	if result.SOCertThumbprint != soKey || len(result.SOCertPEM) == 0 {
		t.Fatalf("re-run returned SO certificate %q, want one for the SO's key %q", result.SOCertThumbprint, soKey)
	}
}
