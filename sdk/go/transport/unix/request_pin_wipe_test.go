package unix

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequestPINWipe: the generic Request consumes the PIN buffers of a
// PIN-carrying request on every return path: a closed transport and a
// method the server refuses.
func TestRequestPINWipe(t *testing.T) {
	cases := map[string]func(t *testing.T) *Transport{
		"closed": func(t *testing.T) *Transport {
			tr := &Transport{config: &transport.Config{}}
			tr.closed.Store(true)
			return tr
		},
		"unknown-method": func(t *testing.T) *Transport {
			tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
			t.Cleanup(cleanup)
			return tr
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			tr := build(t)
			r := &transport.CeremonyInitializeRequest{SOPin: transport.Secret("123456"), UserPin: transport.Secret("654321")}
			if err := tr.Request(context.Background(), "/no.Such/Method", r, nil); err == nil {
				t.Fatal("Request succeeded, want a refusal")
			}
			for i, p := range []transport.Secret{r.SOPin, r.UserPin} {
				for _, b := range p {
					if b != 0 {
						t.Fatalf("PIN %d not wiped: %v", i, []byte(p))
					}
				}
			}
		})
	}
}
