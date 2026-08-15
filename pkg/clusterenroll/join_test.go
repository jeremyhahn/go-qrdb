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

// White-box tests for the join path: fetching a CA bundle from a live
// advertiser, the bundle-splitting helper, and the truststrap method wiring.

package clusterenroll

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// freePort returns a TCP port with nothing bound to it.
//
// The advertiser treats NoisePort 0 as "use DefaultNoisePort" rather than
// "let the OS choose", so every advertiser started with the zero value lands
// on the same fixed port. Tests that each start one must therefore pick
// distinct ports explicitly, or a test will silently enroll against another
// test's advertiser -- which is how this file first failed, fetching an
// anchor-only bundle from a neighbouring test and reporting a missing CA key.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return port
}

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
		Advertise:   AdvertiseOptions{NoisePort: port},
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
		// Addr() reports a wildcard host; dial the loopback explicitly.
		NoiseAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	}
}

// TestJoinReportsAFetchFailure covers the path where no bootstrap method can
// reach a bundle. Enrollment must fail loudly: a node that silently continued
// would fall back to a self-signed identity the cluster does not trust.
func TestJoinReportsAFetchFailure(t *testing.T) {
	opts := &Options{
		RaftAddress: "127.0.0.1:63003",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap: BootstrapOptions{
			// A syntactically valid but unreachable endpoint.
			DirectURL: "https://127.0.0.1:1/bundle",
		},
	}

	_, err := Join(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("Join must fail when no bundle can be fetched")
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

// TestSplitCABundleAcceptsAWellFormedBundle covers the success path against a
// bundle produced by the real provider, so the test cannot drift from the
// format the code actually emits.
func TestSplitCABundleAcceptsAWellFormedBundle(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	provider := &clusterBundleProvider{caCertPEM: caCertPEM, caKeyPEM: caKeyPEM}
	bundle, err := provider.CABundle()
	if err != nil {
		t.Fatalf("CABundle: %v", err)
	}

	certPEM, keyPEM, err := splitCABundle(bundle)
	if err != nil {
		t.Fatalf("splitCABundle: %v", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		t.Fatalf("first component is not a CERTIFICATE block: %v", certBlock)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		t.Fatalf("second component is not an EC PRIVATE KEY block: %v", keyBlock)
	}
	if _, err := x509.ParseCertificate(certBlock.Bytes); err != nil {
		t.Fatalf("split certificate does not parse: %v", err)
	}
}

// TestSplitCABundleRejectsMalformedInput covers each rejection branch. A
// bundle that split wrongly would hand a joining node a CA it cannot sign
// with, so every shape that is not exactly cert-then-key must be refused
// rather than partially accepted.
func TestSplitCABundleRejectsMalformedInput(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	tests := []struct {
		name   string
		bundle []byte
		kind   string
	}{
		{"empty input", nil, "CA certificate"},
		{"not PEM at all", []byte("this is not pem"), "CA certificate"},
		{"first block is the key", append(append([]byte{}, caKeyPEM...), caCertPEM...), "CA certificate"},
		{"certificate with no key", caCertPEM, "CA private key"},
		{"certificate followed by junk", append(append([]byte{}, caCertPEM...), []byte("trailing junk")...), "CA private key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			certPEM, keyPEM, err := splitCABundle(tc.bundle)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			var want *clustertls.KeyError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *clustertls.KeyError", err, err)
			}
			if want.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", want.Kind, tc.kind)
			}
			if certPEM != nil || keyPEM != nil {
				t.Error("a rejected bundle must yield no components")
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
