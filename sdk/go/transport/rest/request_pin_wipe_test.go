package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequestPINWipe: the generic Request consumes the PIN buffers of a
// PIN-carrying request on every return path: a closed transport, a server
// refusing an unknown method, a transport (dial) error and a success.
func TestRequestPINWipe(t *testing.T) {
	cases := map[string]struct {
		build   func(t *testing.T) *Transport
		wantErr bool
	}{
		"closed": {
			build: func(t *testing.T) *Transport {
				srv := httptest.NewServer(http.NotFoundHandler())
				t.Cleanup(srv.Close)
				rt := newWiredTransport(srv)
				rt.closed.Store(true)
				return rt
			},
			wantErr: true,
		},
		"unknown-method": {
			build: func(t *testing.T) *Transport {
				srv := httptest.NewServer(http.NotFoundHandler())
				t.Cleanup(srv.Close)
				return newWiredTransport(srv)
			},
			wantErr: true,
		},
		"transport-error": {
			build:   newRefusedTransport,
			wantErr: true,
		},
		"success": {
			build: func(t *testing.T) *Transport {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(srv.Close)
				return newWiredTransport(srv)
			},
		},
	}
	requests := map[string]func() (any, []transport.Secret){
		"CeremonyInitialize": func() (any, []transport.Secret) {
			r := &transport.CeremonyInitializeRequest{SOPin: transport.Secret("123456"), UserPin: transport.Secret("654321")}
			return r, []transport.Secret{r.SOPin, r.UserPin}
		},
		"BarrierInitialize": func() (any, []transport.Secret) {
			r := &transport.BarrierInitializeRequest{SOPin: transport.Secret("123456"), UserPin: transport.Secret("654321")}
			return r, []transport.Secret{r.SOPin, r.UserPin}
		},
	}
	for name, tc := range cases {
		for method, mk := range requests {
			t.Run(name+"/"+method, func(t *testing.T) {
				rt := tc.build(t)
				req, pins := mk()
				err := rt.Request(context.Background(), method, req, nil)
				if (err != nil) != tc.wantErr {
					t.Fatalf("Request(%s) err = %v, wantErr %v", method, err, tc.wantErr)
				}
				for i, p := range pins {
					for _, b := range p {
						if b != 0 {
							t.Fatalf("PIN %d not wiped: %v", i, []byte(p))
						}
					}
				}
			})
		}
	}
}
