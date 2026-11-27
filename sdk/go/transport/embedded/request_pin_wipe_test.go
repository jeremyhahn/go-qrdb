package embedded

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequestPINWipeOnRefusal: the generic Request consumes the PIN buffers
// of a PIN-carrying request on every refusal path that returns before the
// typed method runs: a closed transport, a rejecting ContextBinder and an
// unknown method name.
func TestRequestPINWipeOnRefusal(t *testing.T) {
	refusals := map[string]func(t *testing.T) (*Transport, string){
		"closed": func(t *testing.T) (*Transport, string) {
			tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(audit.NewNoopLogger()))
			if err != nil {
				t.Fatal(err)
			}
			if err := tr.Close(); err != nil {
				t.Fatal(err)
			}
			return tr, ""
		},
		"binder-refuses": func(t *testing.T) (*Transport, string) {
			var runs atomic.Int32
			tr, err := New(newMockKVService(), &mockAdminService{},
				WithContextBinder(refusingBinder(&runs)), WithAuditLogger(audit.NewNoopLogger()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tr.Close() })
			return tr, ""
		},
		"unknown-method": func(t *testing.T) (*Transport, string) {
			tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(audit.NewNoopLogger()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tr.Close() })
			return tr, "NoSuchMethod"
		},
	}
	requests := map[string]func() (any, []transport.Secret){
		"CeremonyInitialize": func() (any, []transport.Secret) {
			r := &transport.CeremonyInitializeRequest{SOPin: pinBytes(), UserPin: pinBytes()}
			return r, []transport.Secret{r.SOPin, r.UserPin}
		},
		"BarrierInitialize": func() (any, []transport.Secret) {
			r := &transport.BarrierInitializeRequest{SOPin: pinBytes(), UserPin: pinBytes()}
			return r, []transport.Secret{r.SOPin, r.UserPin}
		},
	}
	for refusal, build := range refusals {
		for method, mk := range requests {
			t.Run(refusal+"/"+method, func(t *testing.T) {
				tr, override := build(t)
				name := method
				if override != "" {
					name = override
				}
				req, pins := mk()
				if err := tr.Request(context.Background(), name, req, nil); err == nil {
					t.Fatalf("Request(%s) succeeded, want a refusal", name)
				}
				for i, p := range pins {
					for _, b := range p {
						if b != 0 {
							t.Fatalf("PIN %d not wiped after refusal: %v", i, []byte(p))
						}
					}
				}
			})
		}
	}
}

func pinBytes() transport.Secret { return transport.Secret("123456") }
