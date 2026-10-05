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

package ceremony

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

// TestQuorumCaller_VerifiedChainKeysItsOwnBucket is go-qrdb#591's review
// (blocker 2's consequence) proof: quorumCaller had no direct test at all,
// and its correctness depends entirely on tls.ConnectionState.VerifiedChains
// actually being populated for a caller pkg/clienttrust.Pool wired during
// the sealed window -- exactly what TestPool_Reseal_DropsKeyBTrust
// (pkg/clienttrust) proves the hook now does, by reverting to the static
// baseline config rather than continuing to apply its own VerifyConnection
// override once Key B has dropped. Without that fix, a caller presenting a
// certificate the static baseline (Key A or an operator anchor) verified
// during the sealed window would have reached here with an EMPTY
// VerifiedChains (Pool's own hook, not crypto/tls, would still have been
// doing the verifying) and fallen into the single shared anonymous bucket,
// rather than a bucket of its own.
func TestQuorumCaller_VerifiedChainKeysItsOwnBucket(t *testing.T) {
	leaf := &x509.Certificate{Raw: []byte("a verified leaf, sealed-window or not")}
	ctx := auth.WithTLSState(context.Background(), &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
		VerifiedChains:   [][]*x509.Certificate{{leaf}},
	})

	key, peerThumb, anonymous := quorumCaller(ctx)

	if anonymous {
		t.Error("expected a verified chain to key its own bucket, not the shared anonymous one")
	}
	wantThumb := mtls.Thumbprint(leaf.Raw)
	if peerThumb != wantThumb {
		t.Errorf("peerThumb = %q, want %q", peerThumb, wantThumb)
	}
	wantKey := "tls\x00" + wantThumb
	if key != wantKey {
		t.Errorf("key = %q, want %q", key, wantKey)
	}
}

// TestQuorumCaller_UnverifiedPresentedCertIsAnonymous proves the OTHER
// direction is unchanged: a certificate crypto/tls has NOT verified --
// still possible even outside the sealed window, e.g. an operator mode that
// requests but does not require a client certificate -- never keys a
// bucket of its own; it shares the single anonymous bucket with every
// other unverified caller (see quorumCaller's own doc comment for why).
func TestQuorumCaller_UnverifiedPresentedCertIsAnonymous(t *testing.T) {
	leaf := &x509.Certificate{Raw: []byte("presented but never verified")}
	ctx := auth.WithTLSState(context.Background(), &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
	})

	key, peerThumb, anonymous := quorumCaller(ctx)

	if !anonymous {
		t.Error("expected an unverified presented certificate to be anonymous")
	}
	if key != "anonymous" {
		t.Errorf("key = %q, want %q", key, "anonymous")
	}
	wantThumb := mtls.Thumbprint(leaf.Raw)
	if peerThumb != wantThumb {
		t.Errorf("peerThumb = %q, want %q (the thumbprint is reported regardless of verification, to bind a challenge to possession)", peerThumb, wantThumb)
	}
}

// TestQuorumCaller_NoTLSStateIsAnonymous proves the floor: no TLS state in
// context at all (an embedded or non-mTLS caller) is anonymous, with no
// peer thumbprint to report.
func TestQuorumCaller_NoTLSStateIsAnonymous(t *testing.T) {
	key, peerThumb, anonymous := quorumCaller(context.Background())

	if !anonymous {
		t.Error("expected no TLS state to be anonymous")
	}
	if key != "anonymous" {
		t.Errorf("key = %q, want %q", key, "anonymous")
	}
	if peerThumb != "" {
		t.Errorf("peerThumb = %q, want empty", peerThumb)
	}
}
