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

package clusterenroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// playJoinerRelease makes this process's member run release member and its
// joiners announce release joiner, until the test ends.
func playJoinerRelease(t *testing.T, member, joiner string) {
	t.Helper()
	t.Cleanup(release.SetForTesting(member))
	old := joinerRelease
	joinerRelease = func() string { return joiner }
	t.Cleanup(func() { joinerRelease = old })
}

func TestHandleJoinRequest_RefusesVersionGapBeforeTokenConsumed(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.58.0-alpha"))
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	token := mintTestJoinToken(t, store, "127.0.0.1:9101")
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := buildJoinCSR(nodeKey, "")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	for _, peer := range []string{"0.60.0-alpha", ""} {
		resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
			Token: token, CSRDER: csrDER, ReleaseVersion: peer,
		}, nil)
		if resp.ReleaseVersion != "0.58.0-alpha" || len(resp.CertPEM) != 0 {
			t.Fatalf("peer %q: response = %+v, want a refusal carrying the member's release", peer, resp)
		}
		for _, want := range []string{"enrollment", "0.58.0-alpha", `"` + peer + `"`} {
			if !strings.Contains(resp.Error, want) {
				t.Errorf("peer %q: refusal %q does not name %q", peer, resp.Error, want)
			}
		}
	}

	// The refused joiner's token is still valid.
	if _, err := ConsumeJoinToken(context.Background(), store, testTrustDomain, token); err != nil {
		t.Fatalf("ConsumeJoinToken after a release-gap refusal = %v, want the token still present", err)
	}
}

func TestJoinCSR_GapRefusalIsTyped(t *testing.T) {
	want := release.GapError{Channel: "enrollment", Local: "0.60.0-alpha", Peer: "0.58.0-alpha"}

	t.Run("noise", func(t *testing.T) {
		playJoinerRelease(t, "0.58.0-alpha", "0.60.0-alpha")
		provider := newTestClusterCAProvider(t)
		store := kvstore.NewMemoryKVStore()
		js, err := StartJoinServer(provider, JoinServerOptions{
			Host: testBindHost, Port: freePort(t), Store: store, TrustDomain: testTrustDomain,
		}, t.TempDir(), discardLogger())
		if err != nil {
			t.Fatalf("StartJoinServer: %v", err)
		}
		t.Cleanup(js.Stop)
		token := mintTestJoinToken(t, store, "127.0.0.1:9101")
		nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		csrDER, err := buildJoinCSR(nodeKey, "")
		if err != nil {
			t.Fatalf("buildJoinCSR: %v", err)
		}

		_, err = JoinCSR(context.Background(), &JoinClientOptions{
			ServerAddr: js.Addr(), ServerKey: js.PublicKey(), ServerP256Key: js.P256PublicKey(), Token: token,
		}, csrDER, "")
		var gap *release.GapError
		if !errors.As(err, &gap) || *gap != want {
			t.Fatalf("JoinCSR across a release gap = %v, want %+v", err, want)
		}
	})

	t.Run("http", func(t *testing.T) {
		playJoinerRelease(t, "0.58.0-alpha", "0.60.0-alpha")
		provider := newTestClusterCAProvider(t)
		store := kvstore.NewMemoryKVStore()
		serverURL, pin := joinHTTPServer(t, provider, store)
		token := mintTestJoinToken(t, store, "127.0.0.1:9101")
		_, csrDER := generateTestCSR(t)

		_, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
			ServerURL: serverURL, SPKIPin: pin, Token: token,
		}, csrDER, "")
		var gap *release.GapError
		if !errors.As(err, &gap) || *gap != want {
			t.Fatalf("JoinCSRHTTP across a release gap = %v, want %+v", err, want)
		}
	})

	t.Run("adjacent joins", func(t *testing.T) {
		playJoinerRelease(t, "0.58.0-alpha", "0.59.0-alpha")
		provider := newTestClusterCAProvider(t)
		store := kvstore.NewMemoryKVStore()
		serverURL, pin := joinHTTPServer(t, provider, store)
		token := mintTestJoinToken(t, store, "127.0.0.1:9101")
		nodeURI := "spiffe://" + testTrustDomain + "/node/127.0.0.1:9101"
		_, csrDER := buildMatchingJoinCSR(t, nodeURI)

		if _, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
			ServerURL: serverURL, SPKIPin: pin, Token: token,
		}, csrDER, nodeURI); err != nil {
			t.Fatalf("JoinCSRHTTP between adjacent releases = %v", err)
		}
	})
}
