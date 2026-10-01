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
	"bytes"
	"context"
	"crypto/fips140"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-truststrap/pkg/truststrap"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// ---------------------------------------------------------------------------
// Shared runtime-contract helpers
//
// This file carries NO build constraint, and it must not acquire one: its
// helpers are called from clusterenroll_test.go, resolve_paths_test.go,
// resume_advertiser_test.go, bind_host_test.go, anchor_test.go and
// join_spki_test.go, none of which is constrained either. A helper's
// constraint must be a superset of every caller's (AGENTS.md, "Build
// constraints on test files").
// ---------------------------------------------------------------------------

// forceNoiseAvailable overrides the Noise availability ruling for the duration
// of one test and restores it afterwards.
//
// ONLY false is safe to force in every runtime. Forcing true under
// GODEBUG=fips140=only tells this package that Noise may be used while the Go
// Cryptographic Module still refuses the X25519 underneath it, so any code path
// that reaches a key derivation or a handshake fails on the runtime rather than
// on the thing under test. Use it to assert the FIPS ruling from an ordinary
// runtime; use it with true only where no X25519 operation is performed at all,
// which in practice means buildAutoConfig -- that selector touches no
// cryptography, which is why it exists as a separate function.
func forceNoiseAvailable(t *testing.T, available bool) {
	t.Helper()
	orig := noiseAvailable
	t.Cleanup(func() { noiseAvailable = orig })
	noiseAvailable = func() bool { return available }
}

// assertAdvertises asserts the half of the first-node enrollment contract that
// holds in EVERY runtime, and reports whether this runtime also serves Noise so
// the caller can go on to assert the Noise-specific half.
//
// A caller that returns on a false result has not skipped anything. Under
// GODEBUG=fips140=only there is no X25519 in the process, so a Noise listener
// cannot exist to be asserted about; what exists instead is a different and
// equally definite contract -- the advertiser is present, runs no Noise server,
// binds no address and publishes no key -- and that is what this asserts before
// reporting false. The ordinary CI job runs this suite under fips140=off and
// the nightly FIPS job runs it under fips140=only, so between the two every
// branch below is exercised on every commit.
func assertAdvertises(t *testing.T, adv *Advertiser) bool {
	t.Helper()
	if adv == nil {
		t.Fatal("a first node must get a non-nil Advertiser; nil is how this package says a node owes the cluster no enrollment listener at all")
	}
	if noiseAvailable() {
		if !adv.NoiseEnabled() {
			t.Fatal("Noise is available in this runtime, so the advertiser must be running a Noise listener")
		}
		return true
	}
	if adv.NoiseEnabled() {
		t.Fatal("Noise is unavailable in this runtime, so the advertiser must not be running a Noise listener")
	}
	if got := adv.Addr(); got != "" {
		t.Errorf("Addr() = %q, want empty: no Noise listener is bound in FIPS 140-3 mode", got)
	}
	if got := adv.NoisePublicKey(); len(got) != 0 {
		t.Errorf("NoisePublicKey() = %x, want empty: no Noise key exists in FIPS 140-3 mode", got)
	}
	return false
}

// ---------------------------------------------------------------------------
// The ruling: an operator who named Noise and nothing else is refused
// ---------------------------------------------------------------------------

// TestBuildAutoConfig_RefusesANoiseOnlyJoinUnderFIPS is the fail-closed half of
// the ruling. The operator supplied exactly one bootstrap method, this runtime
// cannot provide it, and there is nothing to select among -- so the join is
// refused before the first dial rather than after a per-method timeout.
func TestBuildAutoConfig_RefusesANoiseOnlyJoinUnderFIPS(t *testing.T) {
	forceNoiseAvailable(t, false)

	opts := &Options{
		Bootstrap: BootstrapOptions{
			NoiseKey:  "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			NoiseAddr: "192.0.2.10:63006",
		},
	}

	cfg, err := buildAutoConfig(opts, discardLogger())
	if err == nil {
		t.Fatalf("buildAutoConfig must refuse a Noise-only join in FIPS 140-3 mode; got cfg = %+v", cfg)
	}
	var fipsErr *NoiseFIPSUnavailableError
	if !errors.As(err, &fipsErr) {
		t.Fatalf("error = %T (%v), want *NoiseFIPSUnavailableError", err, err)
	}
	if fipsErr.Operation == "" {
		t.Error("NoiseFIPSUnavailableError.Operation must name what could not proceed")
	}
}

// TestNoiseFIPSUnavailableError_NamesFIPSRatherThanAKeySize pins the message
// itself. go-truststrap wraps a FIPS refusal in its ErrInvalidKeySize, so the
// message an operator used to get opened with "noise: invalid key size" and
// sent them after a key-length bug that was never there. The replacement has to
// lead with FIPS and name the approved methods, and it must not reintroduce the
// misleading phrase.
func TestNoiseFIPSUnavailableError_NamesFIPSRatherThanAKeySize(t *testing.T) {
	msg := (&NoiseFIPSUnavailableError{Operation: "this node's cluster enrollment join"}).Error()

	for _, want := range []string{"FIPS 140-3", "X25519", "clusterenroll:", "this node's cluster enrollment join"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
	for _, approved := range []string{"--bootstrap-spki-pin", "--bootstrap-dane-dns", "--bootstrap-direct-url"} {
		if !strings.Contains(msg, approved) {
			t.Errorf("message %q does not offer the approved method %q", msg, approved)
		}
	}
	if strings.Contains(msg, "key size") {
		t.Errorf("message %q reintroduces the misleading key-size clause", msg)
	}
	// The FIPS clause must come before anything about keys, so the first line
	// an operator reads is the true cause.
	if i, j := strings.Index(msg, "FIPS 140-3"), strings.Index(msg, "X25519"); i > j {
		t.Errorf("message %q names X25519 before FIPS 140-3; the cause must lead", msg)
	}
}

// TestFetchAnchor_RefusesANoiseOnlyConfigurationBeforeTouchingTheDataDirectory is the
// "at configuration time" half of the ruling. The refusal is worth little if it
// arrives after the node has written a role file or a certificate: the next
// boot would then take the cached path and the operator would be correcting a
// configuration whose consequences are already on disk.
func TestFetchAnchor_RefusesANoiseOnlyConfigurationBeforeTouchingTheDataDirectory(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap: BootstrapOptions{
			NoiseKey:  "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			NoiseAddr: "192.0.2.10:63006",
		},
	}

	anchor, err := FetchAnchor(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatalf("FetchAnchor must refuse a Noise-only configuration in FIPS 140-3 mode; got %q", anchor)
	}
	var fipsErr *NoiseFIPSUnavailableError
	if !errors.As(err, &fipsErr) {
		t.Fatalf("error = %T (%v), want *NoiseFIPSUnavailableError", err, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the refused join wrote %v into the data directory; it must refuse before the first irreversible step", names)
	}
}

// ---------------------------------------------------------------------------
// The ruling: automatic selection over several methods drops Noise, loudly
// ---------------------------------------------------------------------------

// TestBuildAutoConfig_DropsNoiseButKeepsApprovedMethodsUnderFIPS is the
// fail-open-on-purpose half. An operator who listed several methods asked
// truststrap to try them in order, so the approved ones still carry the join.
func TestBuildAutoConfig_DropsNoiseButKeepsApprovedMethodsUnderFIPS(t *testing.T) {
	forceNoiseAvailable(t, false)

	opts := &Options{
		Bootstrap: BootstrapOptions{
			NoiseKey:   "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			NoiseAddr:  "192.0.2.10:63006",
			SPKIPin:    "aabb",
			ServerAddr: "192.0.2.10:9443",
			DirectURL:  "https://192.0.2.10:9443/v1/ca/bootstrap",
		},
	}

	cfg, err := buildAutoConfig(opts, discardLogger())
	if err != nil {
		t.Fatalf("buildAutoConfig: %v", err)
	}
	if cfg.Noise != nil {
		t.Error("cfg.Noise must be nil in FIPS 140-3 mode: a configured Noise server is excluded, not merely deprioritised")
	}
	for _, m := range cfg.MethodOrder {
		if m == truststrap.MethodNoise {
			t.Fatalf("MethodOrder = %v, must not contain the Noise method in FIPS 140-3 mode", cfg.MethodOrder)
		}
	}
	if !containsMethod(cfg.MethodOrder, truststrap.MethodSPKI) || !containsMethod(cfg.MethodOrder, truststrap.MethodDirect) {
		t.Fatalf("MethodOrder = %v, want the approved methods retained", cfg.MethodOrder)
	}
}

// TestBuildAutoConfig_SelectsNoiseWhenItIsAvailable is the regression guard the
// whole ruling hangs on: a node that is NOT in FIPS mode must select Noise
// exactly as it did before this change. It forces availability true, which is
// safe here because buildAutoConfig performs no cryptography -- see
// forceNoiseAvailable -- so this assertion holds in the ordinary CI job and in
// the nightly FIPS job alike.
func TestBuildAutoConfig_SelectsNoiseWhenItIsAvailable(t *testing.T) {
	forceNoiseAvailable(t, true)

	opts := &Options{
		Bootstrap: BootstrapOptions{
			NoiseKey:   "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			NoiseAddr:  "192.0.2.10:63006",
			SPKIPin:    "aabb",
			ServerAddr: "192.0.2.10:9443",
		},
	}

	cfg, err := buildAutoConfig(opts, discardLogger())
	if err != nil {
		t.Fatalf("buildAutoConfig: %v", err)
	}
	if cfg.Noise == nil {
		t.Fatal("cfg.Noise must be populated when Noise is available")
	}
	if cfg.Noise.ServerAddr != opts.Bootstrap.NoiseAddr {
		t.Errorf("cfg.Noise.ServerAddr = %q, want %q", cfg.Noise.ServerAddr, opts.Bootstrap.NoiseAddr)
	}
	if cfg.Noise.ServerStaticKey != opts.Bootstrap.NoiseKey {
		t.Errorf("cfg.Noise.ServerStaticKey = %q, want %q", cfg.Noise.ServerStaticKey, opts.Bootstrap.NoiseKey)
	}
	if len(cfg.MethodOrder) == 0 || cfg.MethodOrder[0] != truststrap.MethodNoise {
		t.Fatalf("MethodOrder = %v, want Noise first exactly as before this change", cfg.MethodOrder)
	}
}

// TestBuildAutoConfig_EmptyMethodListWithoutFIPSIsNotAFIPSFailure guards the
// narrowness of the refusal. A bootstrap configuration can end up with no
// usable method for reasons FIPS has nothing to do with -- an SPKI pin with no
// server address, here -- and answering those with a FIPS error would be a
// wrong diagnosis that happens to fail.
func TestBuildAutoConfig_EmptyMethodListWithoutFIPSIsNotAFIPSFailure(t *testing.T) {
	forceNoiseAvailable(t, true)

	opts := &Options{Bootstrap: BootstrapOptions{SPKIPin: "aabb"}}

	cfg, err := buildAutoConfig(opts, discardLogger())
	if err != nil {
		t.Fatalf("buildAutoConfig: %v", err)
	}
	if len(cfg.MethodOrder) != 0 {
		t.Fatalf("MethodOrder = %v, want empty", cfg.MethodOrder)
	}
}

// TestBuildAutoConfig_NoiseOnlyOutsideFIPSIsNotRefused is the mirror of the
// refusal: the same configuration that fails closed in FIPS mode must be
// accepted everywhere else.
func TestBuildAutoConfig_NoiseOnlyOutsideFIPSIsNotRefused(t *testing.T) {
	forceNoiseAvailable(t, true)

	opts := &Options{
		Bootstrap: BootstrapOptions{
			NoiseKey:  "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			NoiseAddr: "192.0.2.10:63006",
		},
	}

	cfg, err := buildAutoConfig(opts, discardLogger())
	if err != nil {
		t.Fatalf("buildAutoConfig must accept a Noise-only join outside FIPS mode: %v", err)
	}
	if len(cfg.MethodOrder) != 1 || cfg.MethodOrder[0] != truststrap.MethodNoise {
		t.Fatalf("MethodOrder = %v, want exactly the Noise method", cfg.MethodOrder)
	}
}

// ---------------------------------------------------------------------------
// The ruling: a first node still advertises
// ---------------------------------------------------------------------------

// TestStartAdvertiser_UnderFIPSAdvertisesWithoutNoise is the core of the fix.
// Before it, StartAdvertiser returned an error in FIPS mode and a first node
// could not come up at all.
func TestStartAdvertiser_UnderFIPSAdvertisesWithoutNoise(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()

	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost, AdminPort: 9443}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartAdvertiser must succeed in FIPS 140-3 mode: %v", err)
	}
	defer adv.Stop()

	if adv == nil {
		t.Fatal("expected a non-nil Advertiser")
	}
	if adv.NoiseEnabled() {
		t.Error("NoiseEnabled() = true, want false in FIPS 140-3 mode")
	}
	if got := adv.Addr(); got != "" {
		t.Errorf("Addr() = %q, want empty", got)
	}
	if got := adv.NoisePublicKey(); len(got) != 0 {
		t.Errorf("NoisePublicKey() = %x, want empty", got)
	}
	// No Noise static key is generated or persisted. Writing one would put a
	// key produced by a non-approved primitive on the disk of a deployment that
	// has declared it may not use one.
	if fileExists(filepath.Join(dir, NoiseKeyFile)) {
		t.Errorf("%s must not be written in FIPS 140-3 mode", NoiseKeyFile)
	}
}

// TestResolve_UnderFIPSFirstNodeStillAdvertises is the whole-path version: the
// node generates its CA, records its role and comes back advertising.
func TestResolve_UnderFIPSFirstNodeStillAdvertises(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost, AdminPort: 9443},
	}

	mat, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve must succeed for a first node in FIPS 140-3 mode: %v", err)
	}
	defer adv.Stop()

	if mat == nil || len(mat.CACertPEM) == 0 || len(mat.CAKeyPEM) == 0 {
		t.Fatal("expected full CA material")
	}
	if assertAdvertises(t, adv) {
		t.Fatal("forceNoiseAvailable(false) must suppress the Noise listener")
	}

	// The role has to be recorded, because it is what brings the advertiser
	// back on the next boot (go-qrdb#241) -- and the approved enrollment
	// surface is registered by cmd/qrdbd on exactly the condition that a
	// recorded first node produces an Advertiser.
	role, found, err := loadRole(dir)
	if err != nil || !found || role != RoleFirstNode {
		t.Fatalf("loadRole = (%q, %v, %v), want (first-node, true, nil)", role, found, err)
	}
}

// TestResumeAdvertiser_UnderFIPSFirstNodeStillAdvertisesOnRestart covers the
// boot that every first node takes after its first: the cached path. A node
// that advertised on boot 1 and came back silent on boot 2 is go-qrdb#241, and
// the FIPS ruling must not reintroduce it through a different door.
func TestResumeAdvertiser_UnderFIPSFirstNodeStillAdvertisesOnRestart(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost, AdminPort: 9443},
	}

	_, adv1, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	adv1.Stop()

	mat, adv2, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("boot 2 (cached path): %v", err)
	}
	defer adv2.Stop()
	if mat == nil {
		t.Fatal("expected cached material on boot 2")
	}
	if assertAdvertises(t, adv2) {
		t.Fatal("forceNoiseAvailable(false) must suppress the Noise listener on the resumed boot too")
	}
}

// TestResolveCA_UnderFIPSFirstNodeStillAdvertises covers the CA-only resolver,
// which go-xkms uses instead of Resolve because it supplies its own node key.
// It has its own copy of the first-node branch, so it needs its own assertion.
func TestResolveCA_UnderFIPSFirstNodeStillAdvertises(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()

	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost, AdminPort: 9443},
	}

	caCert, caKey, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("ResolveCA must succeed for a first node in FIPS 140-3 mode: %v", err)
	}
	defer adv.Stop()
	if len(caCert) == 0 || len(caKey) == 0 {
		t.Fatal("expected CA certificate and key")
	}
	if assertAdvertises(t, adv) {
		t.Fatal("forceNoiseAvailable(false) must suppress the Noise listener")
	}
}

// TestStartAnchorAdvertiser_UnderFIPSAdvertisesWithoutNoise covers the
// attestation-mode advertiser, which shares startNoiseAdvertiser with the
// cluster-CA one. The anchor bundle is still validated first: FIPS mode
// removes a transport, not a safety check.
func TestStartAnchorAdvertiser_UnderFIPSAdvertisesWithoutNoise(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()

	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	adv, err := StartAnchorAdvertiser(mat.CACertPEM, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartAnchorAdvertiser must succeed in FIPS 140-3 mode: %v", err)
	}
	defer adv.Stop()
	if adv.NoiseEnabled() {
		t.Error("NoiseEnabled() = true, want false in FIPS 140-3 mode")
	}

	// The anchor validation still runs ahead of the FIPS branch: an advertiser
	// that would leak a CA signing key must be refused in every runtime.
	if _, err := StartAnchorAdvertiser(append(append([]byte{}, mat.CACertPEM...), mat.CAKeyPEM...),
		AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, t.TempDir(), discardLogger()); err == nil {
		t.Fatal("an anchor bundle carrying private-key material must still be refused in FIPS 140-3 mode")
	}
}

// ---------------------------------------------------------------------------
// Advertiser.NoiseEnabled
// ---------------------------------------------------------------------------

func TestAdvertiser_NoiseEnabled_NilAndZeroValue(t *testing.T) {
	var nilAdv *Advertiser
	if nilAdv.NoiseEnabled() {
		t.Error("NoiseEnabled() on a nil Advertiser = true, want false")
	}
	if (&Advertiser{}).NoiseEnabled() {
		t.Error("NoiseEnabled() on a zero-value Advertiser = true, want false")
	}
}

func TestAdvertiser_NoiseEnabled_TracksTheRuntime(t *testing.T) {
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartAdvertiser: %v", err)
	}
	defer adv.Stop()

	if got, want := adv.NoiseEnabled(), noiseAvailable(); got != want {
		t.Errorf("NoiseEnabled() = %v, want %v (the runtime's ruling)", got, want)
	}
}

// containsMethod reports whether methods contains m.
func containsMethod(methods []truststrap.Method, m truststrap.Method) bool {
	for _, got := range methods {
		if got == m {
			return true
		}
	}
	return false
}

// assertNoiseFaultUnreachable asserts what StartAdvertiser must do when the
// Noise method is unavailable and the caller's fixture is a fault in the Noise
// static key file or in the listen port: neither is reached, so there is no
// malformed key to reject and no address to collide with, the call succeeds,
// and the advertiser it returns serves no Noise.
//
// Callers use it in place of their fault assertion rather than around it. The
// fault they inject is a property of a code path that a FIPS 140-3 runtime does
// not execute, and "this fixture is not reached" is the strongest true thing
// that can be said about it there -- stronger, in particular, than skipping,
// because a regression that started reading the key file again in FIPS mode
// would fail here.
func assertNoiseFaultUnreachable(t *testing.T, mat *clustertls.Material, opts AdvertiseOptions, dir string) {
	t.Helper()
	adv, err := StartAdvertiser(mat, opts, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartAdvertiser must not reach the Noise key path or bind a port when the method is unavailable: %v", err)
	}
	t.Cleanup(adv.Stop)
	if adv.NoiseEnabled() {
		t.Fatal("NoiseEnabled() = true, want false: the method is unavailable in this runtime")
	}
}

// TestNoiseAvailable_ReportsTheInjectableRuling pins that the exported reader
// and the internal predicate are the same answer. They have to be: cmd/qrdbd
// and go-xkms describe the enrollment surface through the exported one, while
// the selection inside this package uses the internal one, and a surface
// described differently from the surface served is the silent-downgrade shape
// this change exists to remove.
func TestNoiseAvailable_ReportsTheInjectableRuling(t *testing.T) {
	forceNoiseAvailable(t, false)
	if NoiseAvailable() {
		t.Error("NoiseAvailable() = true, want false")
	}
	forceNoiseAvailable(t, true)
	if !NoiseAvailable() {
		t.Error("NoiseAvailable() = false, want true")
	}
}

// TestNoiseAvailable_TracksTheRuntimeByDefault asserts the UNFORCED value,
// which is the one production reads. It is the FIPS module's own answer:
// available exactly when FIPS mode is off.
func TestNoiseAvailable_TracksTheRuntimeByDefault(t *testing.T) {
	if got, want := NoiseAvailable(), !fips140.Enabled(); got != want {
		t.Errorf("NoiseAvailable() = %v, want %v (crypto/fips140.Enabled() = %v)", got, want, fips140.Enabled())
	}
}

// TestBuildAutoConfig_DANEWithoutAnAdminPortIsReportedAsAFetchError covers the
// one branch in the selector that fails for a reason unrelated to FIPS: DANE is
// configured, a peer address is available, and no admin port exists to complete
// the URL with. It must stay a *FetchError naming dane -- the FIPS refusal is
// deliberately narrow, and a selector that answered every empty outcome with
// the FIPS error would be a wrong diagnosis that happens to fail.
func TestBuildAutoConfig_DANEWithoutAnAdminPortIsReportedAsAFetchError(t *testing.T) {
	forceNoiseAvailable(t, false)

	opts := &Options{Bootstrap: BootstrapOptions{
		DANEDNS:         "192.0.2.53:53",
		DiscoveryMethod: "static",
		Peers:           []string{"1=192.0.2.10:63001"},
		// AdminPort deliberately zero: absent, not defaulted.
	}}

	cfg, err := buildAutoConfig(opts, discardLogger())
	if err == nil {
		t.Fatalf("expected a failure; got cfg = %+v", cfg)
	}
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("error = %T (%v), want *FetchError", err, err)
	}
	if fetchErr.Method != "dane" {
		t.Errorf("FetchError.Method = %q, want %q", fetchErr.Method, "dane")
	}
	var fipsErr *NoiseFIPSUnavailableError
	if errors.As(err, &fipsErr) {
		t.Error("a missing admin port must not be reported as a FIPS condition")
	}
}

// TestLogAdvertiserInfo_SaysSoWhenThereIsNoNoiseKey is the "not silent" half of
// the advertise ruling at the layer that reports it. The function used to return
// quietly on a nil key, which under the new behaviour would be a first node
// coming up with a smaller enrollment surface than its operator expects and
// nothing saying so.
func TestLogAdvertiserInfo_SaysSoWhenThereIsNoNoiseKey(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	logAdvertiserInfo(&Advertiser{}, AdvertiseOptions{AdminPort: 9443})

	out := buf.String()
	if out == "" {
		t.Fatal("an advertiser with no Noise identity logged nothing; a downgraded enrollment surface must announce itself")
	}
	for _, want := range []string{"FIPS 140-3", "X25519", "9443", "/v1/ca/bootstrap"} {
		if !strings.Contains(out, want) {
			t.Errorf("the advisory does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "key size") {
		t.Errorf("the advisory reintroduces the misleading key-size clause:\n%s", out)
	}
}

// TestLogAdvertiserInfo_NilAdvertiserLogsNothing is the other side: no
// advertiser means this node owes the cluster no enrollment listener, which is
// not a downgrade and must not produce a FIPS advisory.
func TestLogAdvertiserInfo_NilAdvertiserLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	logAdvertiserInfo(nil, AdvertiseOptions{AdminPort: 9443})

	if out := buf.String(); out != "" {
		t.Errorf("a nil advertiser must log nothing; got:\n%s", out)
	}
}
