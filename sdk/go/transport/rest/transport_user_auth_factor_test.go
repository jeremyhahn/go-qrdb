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
