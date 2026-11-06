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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRotateAuthFactor_refusesAnAddCondition pins that a rotation naming an
// add condition is refused before it leaves the client: the zero Transport
// has no HTTP client, so reaching the wire would panic.
func TestRotateAuthFactor_refusesAnAddCondition(t *testing.T) {
	version := uint64(3)
	for name, newFactor := range map[string]transport.AuthFactorAddRequest{
		"if-no-active-type": {Type: "piv", IfNoActiveType: true},
		"expected-version":  {Type: "piv", ExpectedFactorSetVersion: &version},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (&Transport{}).RotateAuthFactor(context.Background(), &transport.AuthFactorRotateRequest{OldFactorID: "f-1", NewFactor: newFactor})
			if _, ok := errors.AsType[*auth.InvalidFactorError](err); !ok {
				t.Fatalf("err = %v, want an InvalidFactorError", err)
			}
			if reqErr, ok := errors.AsType[*transport.RequestError](err); !ok || reqErr.Method != "RotateAuthFactor" {
				t.Fatalf("err = %T %v, want a RotateAuthFactor RequestError", err, err)
			}
		})
	}
}

func TestRESTUserAuthFactor_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"AddAuthFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{})
			return err
		},
		"ListAuthFactors": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListAuthFactors(ctx, &transport.AuthFactorListRequest{})
			return err
		},
		"GetAuthFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{})
			return err
		},
		"RemoveAuthFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{})
			return err
		},
		"DeleteAuthFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.DeleteAuthFactor(ctx, &transport.AuthFactorDeleteRequest{})
			return err
		},
		"RotateAuthFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{})
			return err
		},
		"RelabelAuthFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{})
			return err
		},
		"LoginAuthFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{})
			return err
		},
	})
}

// TestRESTUserAuthFactor_SendsTheDocumentedRequest pins, per auth-factor RPC,
// the verb, path, query and body the transport sends, and that the answer
// decodes into the RPC's response type.
func TestRESTUserAuthFactor_SendsTheDocumentedRequest(t *testing.T) {
	f7 := transport.AuthFactorRecord{ID: "f7"}
	add := &transport.AuthFactorAddRequest{UserID: 1, Type: "mtls", Credential: []byte("c")}
	rotate := &transport.AuthFactorRotateRequest{OldFactorID: "f1"}
	relabel := &transport.AuthFactorRelabelRequest{FactorID: "f1", Label: "L"}
	login := &transport.AuthFactorLoginRequest{Type: "mtls", LookupKey: []byte("k")}
	marshal := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(data)
	}
	rows := []struct {
		name, verb, path, query, body, answer string
		call                                  func(context.Context, *Transport) (string, error)
	}{
		{"AddAuthFactor", http.MethodPost, "/v1/users/factors", "", marshal(add), marshal(transport.AuthFactorAddResponse{Factor: f7}),
			func(ctx context.Context, rt *Transport) (string, error) {
				r, err := rt.AddAuthFactor(ctx, add)
				if err != nil {
					return "", err
				}
				return r.Factor.ID, nil
			}},
		{"ListAuthFactors", http.MethodGet, "/v1/users/factors/list", "user_id=1", "", marshal(transport.AuthFactorListResponse{Factors: []transport.AuthFactorRecord{f7}, Total: 1}),
			func(ctx context.Context, rt *Transport) (string, error) {
				r, err := rt.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: 1})
				if err != nil || len(r.Factors) != 1 {
					return "", err
				}
				return r.Factors[0].ID, nil
			}},
		{"GetAuthFactor", http.MethodGet, "/v1/users/factors/f1", "", "", marshal(transport.AuthFactorGetResponse{Factor: f7}),
			func(ctx context.Context, rt *Transport) (string, error) {
				r, err := rt.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{FactorID: "f1"})
				if err != nil {
					return "", err
				}
				return r.Factor.ID, nil
			}},
		{"RemoveAuthFactor", http.MethodDelete, "/v1/users/factors/f1", "force=true", "", marshal(transport.AuthFactorRemoveResponse{Status: "f7"}),
			func(ctx context.Context, rt *Transport) (string, error) {
				r, err := rt.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{FactorID: "f1", Force: true})
				if err != nil {
					return "", err
				}
				return r.Status, nil
			}},
		{"RotateAuthFactor", http.MethodPost, "/v1/users/factors/rotate", "", marshal(rotate), marshal(transport.AuthFactorRotateResponse{NewFactor: f7}),
			func(ctx context.Context, rt *Transport) (string, error) {
				r, err := rt.RotateAuthFactor(ctx, rotate)
				if err != nil {
					return "", err
				}
				return r.NewFactor.ID, nil
			}},
		{"RelabelAuthFactor", http.MethodPut, "/v1/users/factors/f1/label", "", marshal(relabel), marshal(transport.AuthFactorRelabelResponse{Factor: f7}),
			func(ctx context.Context, rt *Transport) (string, error) {
				r, err := rt.RelabelAuthFactor(ctx, relabel)
				if err != nil {
					return "", err
				}
				return r.Factor.ID, nil
			}},
		{"LoginAuthFactor", http.MethodPost, "/v1/users/factors/login", "", marshal(login), marshal(transport.AuthFactorLoginResponse{Factor: f7}),
			func(ctx context.Context, rt *Transport) (string, error) {
				r, err := rt.LoginAuthFactor(ctx, login)
				if err != nil {
					return "", err
				}
				return r.Factor.ID, nil
			}},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			var verb, path, query string
			var body []byte
			rs.handle(func(*http.Request) bool { return true }, func(w http.ResponseWriter, r *http.Request) {
				verb, path, query = r.Method, r.URL.Path, r.URL.RawQuery
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.answer)
			})
			id, err := tc.call(context.Background(), rt)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if verb != tc.verb || path != tc.path || query != tc.query {
				t.Fatalf("request = %s %s?%s, want %s %s?%s", verb, path, query, tc.verb, tc.path, tc.query)
			}
			if got := string(bytes.TrimSpace(body)); got != tc.body {
				t.Fatalf("body = %s, want %s", got, tc.body)
			}
			if id != "f7" {
				t.Fatalf("decoded = %q, want f7", id)
			}
		})
	}
}
