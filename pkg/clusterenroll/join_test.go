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

// White-box tests for the fetch path: fetching a CA bundle from a live
// advertiser and the truststrap method wiring.

package clusterenroll

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// freePort returns a TCP port with nothing bound to it.
//
// The advertiser treats NoisePort 0 as "use DefaultNoisePort" rather than
// "let the OS choose", so every advertiser started with the zero value lands
// on the same fixed port. Tests that each start one must therefore pick
// distinct ports explicitly, or a test will silently enroll against another
// test's advertiser -- which is how this file first failed, fetching an
// anchor-only bundle from a neighbouring test and reporting a missing CA key.
//
// The port comes from the shared harness allocator: a "127.0.0.1:0" probe
// closed before the advertiser binds hands out an ephemeral-range number any
// outbound socket -- or another test process -- can take in between.
func freePort(t *testing.T) int {
	t.Helper()
	port, err := qrdbdharness.ReserveTCPPort()
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	return port
}

// testBindHost is the bind host every advertiser in this package's tests
// uses. AdvertiseOptions.NoiseHost has no default -- StartAdvertiser fails
// closed without one (go-qrdb#167) -- and loopback keeps the suite from
// opening a wildcard socket on the build host.
const testBindHost = "127.0.0.1"

// startFirstNode brings up a real first node (path 5): it generates the
// cluster CA and starts the Noise advertiser on a port of its own. The
// returned options are what a joining node needs to reach it.
func startFirstNode(t *testing.T) (*Advertiser, BootstrapOptions) {
	t.Helper()

	port := freePort(t)
	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: port, NoiseHost: testBindHost},
	}

	_, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("first node Resolve: %v", err)
	}
	if adv == nil {
		t.Fatal("first node must return an advertiser")
	}
	t.Cleanup(adv.Stop)

	return adv, BootstrapOptions{
		NoiseKey: hex.EncodeToString(adv.NoisePublicKey()),
		// Addr() now reports the host the advertiser was actually bound
		// to, so the joining node dials exactly that.
		NoiseAddr: adv.Addr(),
	}
}

// TestFetchAnchorReportsAFetchFailure covers the path where no bootstrap
// method can reach a bundle. It must fail loudly: a node that silently
// continued would fall back to a self-signed identity the cluster does not
// trust.
func TestFetchAnchorReportsAFetchFailure(t *testing.T) {
	opts := &Options{
		RaftAddress: "127.0.0.1:63003",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap: BootstrapOptions{
			// A syntactically valid but unreachable endpoint.
			DirectURL: "https://127.0.0.1:1/bundle",
		},
	}

	_, err := FetchAnchor(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("FetchAnchor must fail when no bundle can be fetched")
	}
	var want *FetchError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *FetchError", err, err)
	}
}

// TestAutoFetchBundleWiresEveryConfiguredMethod covers the per-method config
// branches. Each fetch is expected to FAIL (nothing is listening) -- what is
// under test is that the option shape produces a configured method at all
// rather than being silently dropped, which would make an operator's
// --bootstrap-spki flag a no-op that reports no error.
func TestAutoFetchBundleWiresEveryConfiguredMethod(t *testing.T) {
	tests := []struct {
		name      string
		bootstrap BootstrapOptions
	}{
		{
			name:      "spki",
			bootstrap: BootstrapOptions{SPKIPin: "aGVsbG8=", ServerAddr: "127.0.0.1:1"},
		},
		{
			name:      "dane",
			bootstrap: BootstrapOptions{DANEDNS: "127.0.0.1:1", ServerAddr: "127.0.0.1:1"},
		},
		{
			name:      "direct",
			bootstrap: BootstrapOptions{DirectURL: "https://127.0.0.1:1/bundle"},
		},
		{
			name: "several at once",
			bootstrap: BootstrapOptions{
				SPKIPin:    "aGVsbG8=",
				ServerAddr: "127.0.0.1:1",
				DirectURL:  "https://127.0.0.1:1/bundle",
				DANEDNS:    "127.0.0.1:1",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{
				RaftAddress: "127.0.0.1:63004",
				TrustDomain: "test.cluster",
				DataDir:     t.TempDir(),
				Bootstrap:   tc.bootstrap,
			}
			_, err := autoFetchBundle(context.Background(), opts, discardLogger())
			if err == nil {
				t.Fatal("expected the fetch to fail against a dead endpoint")
			}
			var want *FetchError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *FetchError", err, err)
			}
		})
	}
}

// TestLogAdvertiserInfoToleratesAbsentAdvertisers covers the nil guards. This
// runs on the startup path, so a panic here would take down a node that had
// otherwise enrolled successfully.
func TestLogAdvertiserInfoToleratesAbsentAdvertisers(t *testing.T) {
	logAdvertiserInfo(nil, AdvertiseOptions{})
	logAdvertiserInfo(&Advertiser{}, AdvertiseOptions{})
}

// TestLogAdvertiserInfoDefaultsTheAdminPort covers the zero-port branch, where
// the logged value is the port the server will actually use rather than the
// literal zero the operator did not set.
func TestLogAdvertiserInfoDefaultsTheAdminPort(t *testing.T) {
	adv, _ := startFirstNode(t)

	logAdvertiserInfo(adv, AdvertiseOptions{AdminPort: 0})
	logAdvertiserInfo(adv, AdvertiseOptions{AdminPort: 9443})
}
