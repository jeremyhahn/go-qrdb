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

// Tests for the Noise enrollment listener's bind host (go-qrdb#167 item 3).
// The enrollment listener is the one socket a joining node reaches before it
// holds any credential, so where it binds is a containment decision, and the
// assertions here are against the address the server actually bound.
package clusterenroll

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// TestStartAdvertiser_BindsConfiguredHost: the configured host is where the
// listener lands. Previously the listen address was assembled from a wildcard
// literal and the port, so a node contained to one interface opened enrollment
// on all of them.
func TestStartAdvertiser_BindsConfiguredHost(t *testing.T) {
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	port := freePort(t)

	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: port, NoiseHost: "127.0.0.1"}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAdvertiser: %v", err)
	}
	t.Cleanup(adv.Stop)

	host, gotPort, err := net.SplitHostPort(adv.Addr())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", adv.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("Noise listener bound host %q, want 127.0.0.1: the configured bind host did not reach the listener", host)
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		t.Fatalf("Noise listener bound the wildcard host %q", host)
	}
	if gotPort != strconv.Itoa(port) {
		t.Fatalf("Noise listener bound port %q, want %d", gotPort, port)
	}
}

// TestStartAdvertiser_FailsClosedWithoutBindHost: an absent bind host is not
// an invitation to pick one. The package cannot know which containment the
// operator asked for, so it refuses rather than opening the enrollment
// listener on every interface.
//
// It also asserts the rejection has no side effect. Configuration is checked
// before the key branch runs, so a refused configuration must not leave a
// freshly generated Noise static key -- this node's long-lived enrollment
// identity, which joining nodes are told to expect -- sitting in the data
// directory.
func TestStartAdvertiser_FailsClosedWithoutBindHost(t *testing.T) {
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	dir := t.TempDir()

	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: freePort(t)}, dir, discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("StartAdvertiser returned an advertiser with no bind host configured")
	}
	var missing *MissingBindHostError
	if !errors.As(err, &missing) {
		t.Fatalf("StartAdvertiser error = %v (%T), want *MissingBindHostError", err, err)
	}
	if fileExists(filepath.Join(dir, NoiseKeyFile)) {
		t.Fatalf("a rejected configuration persisted %s; the bind host must be checked before any key material is generated", NoiseKeyFile)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir(%q): %v", dir, readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("a rejected configuration left %d entries in the data directory, want none", len(entries))
	}
}

// TestResolve_FirstNode_NoMaterialWhenBindHostMissing is the more important
// half of the fail-closed contract, and the one a test on StartAdvertiser
// alone would miss.
//
// Resolve path 5 generates the cluster CA, persists it, and only then starts
// the advertiser. If the rejection happened at the advertiser, the CA would
// already be on disk -- and a second boot with the configuration corrected
// takes path 2 (cached certificates), which returns NO advertiser. The node
// would come up looking healthy, forever unable to enroll a peer, with no way
// for the operator to see it from configuration. So the advertiser
// configuration is validated before any material exists.
func TestResolve_FirstNode_NoMaterialWhenBindHostMissing(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t)}, // no NoiseHost
	}

	mat, adv, err := Resolve(context.Background(), opts, discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("Resolve returned an advertiser with no bind host configured")
	}
	if mat != nil {
		t.Fatal("Resolve returned material for a configuration it refused")
	}
	var missing *MissingBindHostError
	if !errors.As(err, &missing) {
		t.Fatalf("Resolve error = %v (%T), want *MissingBindHostError", err, err)
	}
	assertDirEmpty(t, dir, "Resolve")
}

// TestResolveCA_FirstNode_NoMaterialWhenBindHostMissing is the same contract
// on the CA-only path, which persists via PersistCA rather than
// PersistMaterial and is reached by consumers that issue their own leaf.
func TestResolveCA_FirstNode_NoMaterialWhenBindHostMissing(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t)}, // no NoiseHost
	}

	caCertPEM, caKeyPEM, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("ResolveCA returned an advertiser with no bind host configured")
	}
	if caCertPEM != nil || caKeyPEM != nil {
		t.Fatal("ResolveCA returned CA material for a configuration it refused")
	}
	var missing *MissingBindHostError
	if !errors.As(err, &missing) {
		t.Fatalf("ResolveCA error = %v (%T), want *MissingBindHostError", err, err)
	}
	assertDirEmpty(t, dir, "ResolveCA")
}

// assertDirEmpty fails when a refused configuration left anything behind. It
// is the whole point of these two tests: the error type is cheap to get right
// and the absence of durable state is what actually keeps the node
// recoverable.
func assertDirEmpty(t *testing.T, dir, who string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	if len(entries) == 0 {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Fatalf("%s refused the configuration but left %v in the data directory; a later boot would take the cached-material path and start no advertiser", who, names)
}

// TestStartAnchorAdvertiser_FailsClosedWithoutBindHost: the anchor advertiser
// shares startNoiseAdvertiser, so it inherits the same contract. Asserted
// separately because it is a distinct exported entry point.
func TestStartAnchorAdvertiser_FailsClosedWithoutBindHost(t *testing.T) {
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	adv, err := StartAnchorAdvertiser(mat.CACertPEM, AdvertiseOptions{NoisePort: freePort(t)}, t.TempDir(), discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("StartAnchorAdvertiser returned an advertiser with no bind host configured")
	}
	var missing *MissingBindHostError
	if !errors.As(err, &missing) {
		t.Fatalf("StartAnchorAdvertiser error = %v (%T), want *MissingBindHostError", err, err)
	}
}
