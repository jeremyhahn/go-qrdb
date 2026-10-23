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

package server

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// stubChainVerifier is a chain verifier no test consults.
type stubChainVerifier struct{}

func (stubChainVerifier) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	return nil, errors.New("not reached")
}
func (stubChainVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }
func (stubChainVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return errors.New("not reached")
}

var _ auth.IssuerVerifier = stubChainVerifier{}

// TestWithPeerChainVerifier_SetsTheVerifier: the option records the verifier
// every transport is handed, and a later nil clears it.
func TestWithPeerChainVerifier_SetsTheVerifier(t *testing.T) {
	var v auth.IssuerVerifier = stubChainVerifier{}
	o := &serverOptions{}
	WithPeerChainVerifier(v)(o)
	if o.peerChainVerifier != v {
		t.Fatalf("peerChainVerifier = %v, want the option's verifier", o.peerChainVerifier)
	}
	WithPeerChainVerifier(nil)(o)
	if o.peerChainVerifier != nil {
		t.Fatalf("peerChainVerifier = %v after a nil option, want nil", o.peerChainVerifier)
	}
}

// TestPeerChainOptions_ForwardsTheVerifier: a set verifier becomes exactly
// one transport option, built from that verifier.
func TestPeerChainOptions_ForwardsTheVerifier(t *testing.T) {
	var v auth.IssuerVerifier = stubChainVerifier{}
	var handed []auth.IssuerVerifier
	opts := peerChainOptions(v, func(got auth.IssuerVerifier) string {
		handed = append(handed, got)
		return "option"
	})
	if len(opts) != 1 || opts[0] != "option" {
		t.Fatalf("options = %v, want the one built from the verifier", opts)
	}
	if len(handed) != 1 || handed[0] != v {
		t.Fatalf("transport option handed %v, want the verifier once", handed)
	}
}

// TestPeerChainOptions_NilVerifierAddsNothing: with no verifier the transport
// gets no option, so it keeps its AuthMiddleware's issuer verifier rather
// than having it overridden by nil.
func TestPeerChainOptions_NilVerifierAddsNothing(t *testing.T) {
	opts := peerChainOptions(nil, func(auth.IssuerVerifier) string {
		t.Fatal("transport option built for a nil verifier")
		return ""
	})
	if len(opts) != 0 {
		t.Fatalf("options = %v, want none", opts)
	}
}
