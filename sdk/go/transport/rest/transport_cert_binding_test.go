// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTCertBinding_SendsTheDocumentedRequest pins each certificate-binding
// RPC to its verb, path and body on the wire, and checks the user answer is
// decoded.
func TestRESTCertBinding_SendsTheDocumentedRequest(t *testing.T) {
	checkUser := func(u *transport.User, err error) error {
		if err == nil && (u.ID != 42 || u.Username != "alice") {
			return errors.New("user not decoded, want 42 alice")
		}
		return err
	}
	alice := transport.User{ID: 42, Username: "alice"}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "GetUserByCertThumbprint", verb: http.MethodGet, path: "/users/by-cert-thumbprint/abc123", answer: alice,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.GetUserByCertThumbprint(ctx, "abc123"))
			}},
		{name: "AddCertBinding", verb: http.MethodPost, path: "/users/42/cert-bindings", answer: alice,
			wantBody: map[string]any{"thumbprint": "abc123", "not_after_unix": 1700000000},
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.AddCertBinding(ctx, 42, "abc123", 1700000000))
			}},
		{name: "RemoveCertBinding", verb: http.MethodDelete, path: "/users/42/cert-bindings/abc123", answer: alice,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.RemoveCertBinding(ctx, 42, "abc123"))
			}},
	})
}

// TestRESTCertBinding_NotFoundIsTyped checks a 404 surfaces as a
// UserNotFoundError naming the lookup that missed: the thumbprint for a
// lookup by certificate, the user ID for a binding change.
func TestRESTCertBinding_NotFoundIsTyped(t *testing.T) {
	cases := []struct {
		name, verb, path string
		want             transport.UserNotFoundError
		call             func(ctx context.Context, rt *Transport) error
	}{
		{"GetUserByCertThumbprint", http.MethodGet, "/users/by-cert-thumbprint/abc123",
			transport.UserNotFoundError{Lookup: "cert_spki/abc123"}, func(ctx context.Context, rt *Transport) error {
				_, err := rt.GetUserByCertThumbprint(ctx, "abc123")
				return err
			}},
		{"AddCertBinding", http.MethodPost, "/users/42/cert-bindings",
			transport.UserNotFoundError{ID: 42}, func(ctx context.Context, rt *Transport) error {
				_, err := rt.AddCertBinding(ctx, 42, "abc123", 0)
				return err
			}},
		{"RemoveCertBinding", http.MethodDelete, "/users/42/cert-bindings/abc123",
			transport.UserNotFoundError{ID: 42}, func(ctx context.Context, rt *Transport) error {
				_, err := rt.RemoveCertBinding(ctx, 42, "abc123")
				return err
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			rs.handle(exactPath(c.verb, c.path), func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "no such user", http.StatusNotFound)
			})

			err := c.call(context.Background(), rt)
			var nf *transport.UserNotFoundError
			if !errors.As(err, &nf) || *nf != c.want {
				t.Fatalf("err = %v, want %+v", err, c.want)
			}
		})
	}
}

// TestRESTCertBinding_SurfacesAFailedAnswer checks every certificate-binding
// RPC surfaces a refused or undecodable answer as a RequestError naming it.
func TestRESTCertBinding_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"GetUserByCertThumbprint", http.MethodGet, "/users/by-cert-thumbprint/abc123", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetUserByCertThumbprint(ctx, "abc123")
			return err
		}},
		{"AddCertBinding", http.MethodPost, "/users/42/cert-bindings", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.AddCertBinding(ctx, 42, "abc123", 0)
			return err
		}},
		{"RemoveCertBinding", http.MethodDelete, "/users/42/cert-bindings/abc123", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.RemoveCertBinding(ctx, 42, "abc123")
			return err
		}},
	})
}

// TestRESTCertBinding_Closed checks every certificate-binding RPC refuses on a
// closed transport before anything reaches the wire.
func TestRESTCertBinding_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"GetUserByCertThumbprint": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetUserByCertThumbprint(ctx, "abc123")
			return err
		},
		"AddCertBinding": func(ctx context.Context, rt *Transport) error {
			_, err := rt.AddCertBinding(ctx, 42, "abc123", 0)
			return err
		},
		"RemoveCertBinding": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RemoveCertBinding(ctx, 42, "abc123")
			return err
		},
	})
}
