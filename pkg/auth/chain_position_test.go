// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// chainStub is an Authenticator identified only by its name.
type chainStub string

func (s chainStub) Authenticate(context.Context) (*Identity, error) { return nil, ErrUnauthenticated }
func (s chainStub) Name() string                                    { return string(s) }

func chainNames(chain []Authenticator) string {
	names := make([]string, len(chain))
	for i, a := range chain {
		names[i] = a.Name()
	}
	return strings.Join(names, ",")
}

func TestAuthenticatorChain_Orders(t *testing.T) {
	mtls := chainStub("mtls")
	cases := []struct {
		name       string
		placements []AuthenticatorPlacement
		want       string
	}{
		{"none", nil, "mtls"},
		{"after", []AuthenticatorPlacement{{ChainAfterMTLS, chainStub("a1")}, {ChainAfterMTLS, chainStub("a2")}}, "mtls,a1,a2"},
		{"before", []AuthenticatorPlacement{{ChainBeforeMTLS, chainStub("b1")}, {ChainBeforeMTLS, chainStub("b2")}}, "b1,b2,mtls"},
		{"replace", []AuthenticatorPlacement{{ChainReplaceMTLS, chainStub("r1")}, {ChainReplaceMTLS, chainStub("r2")}}, "r1,r2"},
		{"mixed", []AuthenticatorPlacement{
			{ChainAfterMTLS, chainStub("a1")},
			{ChainBeforeMTLS, chainStub("b1")},
			{ChainAfterMTLS, chainStub("a2")},
			{ChainBeforeMTLS, chainStub("b2")},
		}, "b1,b2,mtls,a1,a2"},
		{"replace takes the mtls slot", []AuthenticatorPlacement{
			{ChainAfterMTLS, chainStub("a1")},
			{ChainReplaceMTLS, chainStub("r1")},
			{ChainBeforeMTLS, chainStub("b1")},
		}, "b1,r1,a1"},
		{"nil authenticators install nothing", []AuthenticatorPlacement{{ChainBeforeMTLS, nil}, {ChainAfterMTLS, nil}}, "mtls"},
		{"a nil replacement still removes mtls", []AuthenticatorPlacement{{ChainReplaceMTLS, nil}, {ChainAfterMTLS, chainStub("a1")}}, "a1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain, err := AuthenticatorChain(mtls, tc.placements)
			if err != nil {
				t.Fatalf("AuthenticatorChain: %v", err)
			}
			if got := chainNames(chain); got != tc.want {
				t.Fatalf("chain = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthenticatorChain_RefusesInvalidPosition(t *testing.T) {
	for _, pos := range []ChainPosition{0, ChainReplaceMTLS + 1, -1} {
		_, err := AuthenticatorChain(chainStub("mtls"), []AuthenticatorPlacement{
			{ChainAfterMTLS, chainStub("ok")},
			{pos, chainStub("bad")},
		})
		var target *InvalidChainPositionError
		if !errors.As(err, &target) {
			t.Fatalf("position %d: err = %v, want *InvalidChainPositionError", pos, err)
		}
		if target.Index != 1 || target.Position != pos {
			t.Fatalf("position %d: error names placement %d position %d", pos, target.Index, target.Position)
		}
		if !strings.Contains(err.Error(), "placement 1") {
			t.Fatalf("message does not name the placement: %q", err.Error())
		}
	}
}

func TestAuthenticatorChain_RefusesInvalidPositionWithNilAuthenticator(t *testing.T) {
	_, err := AuthenticatorChain(chainStub("mtls"), []AuthenticatorPlacement{{Authenticator: nil}})
	var target *InvalidChainPositionError
	if !errors.As(err, &target) {
		t.Fatalf("err = %v, want *InvalidChainPositionError", err)
	}
}

func TestAuthenticatorChain_RefusesEmptyChain(t *testing.T) {
	for _, placements := range [][]AuthenticatorPlacement{
		{{ChainReplaceMTLS, nil}},
		{{ChainReplaceMTLS, nil}, {ChainBeforeMTLS, nil}, {ChainAfterMTLS, nil}},
	} {
		_, err := AuthenticatorChain(chainStub("mtls"), placements)
		var target *EmptyAuthenticatorChainError
		if !errors.As(err, &target) {
			t.Fatalf("err = %v, want *EmptyAuthenticatorChainError", err)
		}
		if err.Error() == "" {
			t.Fatal("empty error message")
		}
	}
}

func TestChainPosition_String(t *testing.T) {
	cases := map[ChainPosition]string{
		ChainBeforeMTLS:  "before-mtls",
		ChainAfterMTLS:   "after-mtls",
		ChainReplaceMTLS: "replace-mtls",
		0:                "chain-position(0)",
		42:               "chain-position(42)",
	}
	for pos, want := range cases {
		if got := pos.String(); got != want {
			t.Errorf("ChainPosition(%d).String() = %q, want %q", int(pos), got, want)
		}
	}
}
