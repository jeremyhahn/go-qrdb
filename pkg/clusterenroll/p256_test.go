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
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/fips140"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"
	"github.com/jeremyhahn/go-truststrap/pkg/p256proto"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// This file carries NO build constraint: it calls helpers from fips_test.go
// and join_csr_test.go, which carry none (AGENTS.md, "Build constraints on test
// files"). Nothing here skips under FIPS: the P-256 suite is the one that works
// in every runtime, so every test in this file runs under fips140=only as well.

// ---------------------------------------------------------------------------
// Key persistence
// ---------------------------------------------------------------------------

func TestLoadOrGenerateP256Key_PersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", P256KeyFile)

	first, err := loadOrGenerateP256Key(path, "noise", "bootstrap", discardLogger())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	defer first.Destroy()
	if got := len(first.PublicBytes()); got != p256proto.PublicKeySize {
		t.Fatalf("public key = %d bytes, want %d", got, p256proto.PublicKeySize)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key file not persisted: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	if info.Size() != p256proto.HexKeySize {
		t.Errorf("key file size = %d, want %d hex characters", info.Size(), p256proto.HexKeySize)
	}

	second, err := loadOrGenerateP256Key(path, "noise", "bootstrap", discardLogger())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer second.Destroy()
	if hex.EncodeToString(first.PublicBytes()) != hex.EncodeToString(second.PublicBytes()) {
		t.Fatal("a reloaded key has a different public key: the persisted scalar did not round-trip")
	}
}

func TestLoadOrGenerateP256Key_RejectsAWrongSizeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), P256KeyFile)
	if err := os.WriteFile(path, []byte("abcd"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	key, err := loadOrGenerateP256Key(path, "join", "join", discardLogger())
	if err == nil {
		key.Destroy()
		t.Fatal("a truncated key file must be refused, never regenerated over")
	}
	var sizeErr *P256KeyFileSizeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("error = %T (%v), want *P256KeyFileSizeError", err, err)
	}
	if sizeErr.Got != 4 || sizeErr.Want != p256proto.HexKeySize || sizeErr.Path != path {
		t.Errorf("P256KeyFileSizeError = %+v", sizeErr)
	}
	var srvErr *ServerError
	if !errors.As(err, &srvErr) || srvErr.Protocol != "join" {
		t.Errorf("error = %v, want a *ServerError labelled join", err)
	}
	if onDisk, _ := os.ReadFile(path); string(onDisk) != "abcd" {
		t.Error("the refused key file was overwritten")
	}
}

func TestLoadOrGenerateP256Key_RejectsNonHexContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), P256KeyFile)
	bad := make([]byte, p256proto.HexKeySize)
	for i := range bad {
		bad[i] = 'z'
	}
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if key, err := loadOrGenerateP256Key(path, "noise", "bootstrap", discardLogger()); err == nil {
		key.Destroy()
		t.Fatal("a key file that is not hex must be refused")
	}
}

func TestLoadOrGenerateP256Key_ReadAndWriteFailures(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), P256KeyFile)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		orig := readFileFn
		t.Cleanup(func() { readFileFn = orig })
		wantErr := errors.New("read refused")
		readFileFn = func(string) ([]byte, error) { return nil, wantErr }

		key, err := loadOrGenerateP256Key(path, "noise", "bootstrap", discardLogger())
		if err == nil {
			key.Destroy()
			t.Fatal("expected a read failure")
		}
		if !errors.Is(err, wantErr) {
			t.Errorf("error = %v, want it to wrap the read failure", err)
		}
	})

	t.Run("mkdir", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		key, err := loadOrGenerateP256Key(filepath.Join(blocker, P256KeyFile), "noise", "bootstrap", discardLogger())
		if err == nil {
			key.Destroy()
			t.Fatal("expected a failure creating the key directory")
		}
	})
}

func TestP256KeyFileSizeError_Message(t *testing.T) {
	msg := (&P256KeyFileSizeError{Path: "/k", Got: 3, Want: 64}).Error()
	for _, want := range []string{"/k", "3", "64"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// ---------------------------------------------------------------------------
// The anchor advertiser serves P-256
// ---------------------------------------------------------------------------

// TestFetchAnchor_OverP256 is the FIPS fetch: the joiner is configured with the
// P-256 key alone, which is what a node under fips140=only can use. It runs in
// every runtime, and in an ordinary one forceNoiseAvailable(false) makes the
// advertiser and the joiner both behave as they would under FIPS.
func TestFetchAnchor_OverP256(t *testing.T) {
	forceNoiseAvailable(t, false)
	caCertPEM, _ := anchorTestCA(t)

	adv, err := StartAnchorAdvertiser(caCertPEM, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAnchorAdvertiser: %v", err)
	}
	defer adv.Stop()

	fetched, err := FetchAnchor(context.Background(), &Options{Bootstrap: BootstrapOptions{
		P256Key:   hex.EncodeToString(adv.P256PublicKey()),
		NoiseAddr: adv.Addr(),
	}}, discardLogger())
	if err != nil {
		t.Fatalf("FetchAnchor over P-256: %v", err)
	}
	if string(fetched) != string(caCertPEM) {
		t.Error("the anchor fetched over P-256 does not match the advertised anchor")
	}
}

// TestFetchAnchor_OverBothSuites runs the same fetch with both keys configured
// and Noise available: the client picks a suite by policy and either way the
// advertiser's one listener answers. Where Noise is not available (a FIPS
// runtime) the configuration is the dual-key one an operator would write and the
// P-256 suite carries it, which is the point.
func TestFetchAnchor_OverBothSuites(t *testing.T) {
	caCertPEM, _ := anchorTestCA(t)

	adv, err := StartAnchorAdvertiser(caCertPEM, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAnchorAdvertiser: %v", err)
	}
	defer adv.Stop()

	boot := BootstrapOptions{
		P256Key:   hex.EncodeToString(adv.P256PublicKey()),
		NoiseAddr: adv.Addr(),
	}
	if adv.NoiseEnabled() {
		boot.NoiseKey = hex.EncodeToString(adv.NoisePublicKey())
	}
	fetched, err := FetchAnchor(context.Background(), &Options{Bootstrap: boot}, discardLogger())
	if err != nil {
		t.Fatalf("FetchAnchor: %v", err)
	}
	if string(fetched) != string(caCertPEM) {
		t.Error("fetched anchor does not match the advertised anchor")
	}
}

// TestFetchAnchor_DualKeyBootstrapTracksTheRuntime configures both suites'
// keys and leaves the gating decision to the runtime. Under crypto/fips140 the
// Noise key never reaches go-truststrap, so its client holds no key it could
// run X25519 against, and the fetch completes over P-256.
func TestFetchAnchor_DualKeyBootstrapTracksTheRuntime(t *testing.T) {
	caCertPEM, _ := anchorTestCA(t)

	adv, err := StartAnchorAdvertiser(caCertPEM, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAnchorAdvertiser: %v", err)
	}
	defer adv.Stop()

	noiseKey := make([]byte, 32)
	if adv.NoiseEnabled() {
		noiseKey = adv.NoisePublicKey()
	}
	opts := &Options{Bootstrap: BootstrapOptions{
		NoiseKey:  hex.EncodeToString(noiseKey),
		P256Key:   hex.EncodeToString(adv.P256PublicKey()),
		NoiseAddr: adv.Addr(),
	}}

	cfg, err := buildAutoConfig(opts, discardLogger())
	if err != nil {
		t.Fatalf("buildAutoConfig: %v", err)
	}
	if cfg.Noise == nil || cfg.Noise.ServerP256StaticKey != opts.Bootstrap.P256Key {
		t.Fatal("the P-256 key must be offered in every runtime")
	}
	if offered := cfg.Noise.ServerStaticKey != ""; offered == fips140.Enabled() {
		t.Fatalf("Noise key offered = %v with crypto/fips140.Enabled() = %v", offered, fips140.Enabled())
	}

	fetched, err := FetchAnchor(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("FetchAnchor: %v", err)
	}
	if string(fetched) != string(caCertPEM) {
		t.Error("fetched anchor does not match the advertised anchor")
	}
}

// TestFetchAnchor_WrongP256KeyFails pins that the key is the trust anchor: a
// joiner holding a different key than the listener's must not obtain a bundle.
func TestFetchAnchor_WrongP256KeyFails(t *testing.T) {
	forceNoiseAvailable(t, false)
	caCertPEM, _ := anchorTestCA(t)

	adv, err := StartAnchorAdvertiser(caCertPEM, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAnchorAdvertiser: %v", err)
	}
	defer adv.Stop()

	other, err := p256proto.GenerateStaticKey()
	if err != nil {
		t.Fatalf("GenerateStaticKey: %v", err)
	}
	defer other.Destroy()

	opts := &Options{Bootstrap: BootstrapOptions{
		P256Key:   hex.EncodeToString(other.PublicBytes()),
		NoiseAddr: adv.Addr(),
	}}
	if fetched, err := FetchAnchor(context.Background(), opts, discardLogger()); err == nil {
		t.Fatalf("FetchAnchor with the wrong P-256 key returned %q", fetched)
	}
}

func TestStartAdvertiser_ReusesThePersistedP256Key(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}

	start := func() []byte {
		adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, dir, discardLogger())
		if err != nil {
			t.Fatalf("StartAdvertiser: %v", err)
		}
		defer adv.Stop()
		return adv.P256PublicKey()
	}
	if a, b := start(), start(); hex.EncodeToString(a) != hex.EncodeToString(b) {
		t.Fatal("a restarted advertiser published a different P-256 key; every joining node's configured anchor would break")
	}
}

func TestStartAdvertiser_RefusesACorruptP256KeyFile(t *testing.T) {
	forceNoiseAvailable(t, false)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, P256KeyFile), []byte("short"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, dir, discardLogger())
	if err == nil {
		adv.Stop()
		t.Fatal("StartAdvertiser must refuse a corrupt P-256 key file")
	}
	var sizeErr *P256KeyFileSizeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("error = %T (%v), want *P256KeyFileSizeError", err, err)
	}
}

func TestAdvertiser_P256PublicKey_NilAndZeroValue(t *testing.T) {
	var nilAdv *Advertiser
	if nilAdv.P256PublicKey() != nil {
		t.Error("P256PublicKey() on a nil Advertiser must be nil")
	}
	if (&Advertiser{}).P256PublicKey() != nil {
		t.Error("P256PublicKey() on a zero-value Advertiser must be nil")
	}
}

// ---------------------------------------------------------------------------
// The join exchange over both suites
// ---------------------------------------------------------------------------

// newTestP256PublicKey returns a well-formed P-256 responder public key whose
// private half is discarded, for tests that need a key the client accepts but
// never reach a handshake with it.
func newTestP256PublicKey(t *testing.T) []byte {
	t.Helper()
	key, err := p256proto.GenerateStaticKey()
	if err != nil {
		t.Fatalf("GenerateStaticKey: %v", err)
	}
	defer key.Destroy()
	return key.PublicBytes()
}

// startJoinServerForSuite starts a join listener and mints a token for it. Noise
// availability is the caller's to force beforehand.
func startJoinServerForSuite(t *testing.T) (*JoinServer, kvstore.AtomicKVStore, string) {
	t.Helper()
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: freePort(t), Store: store, TrustDomain: testTrustDomain,
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)
	return js, store, mintTestJoinToken(t, store, "127.0.0.1:9100")
}

func joinCSRFor(t *testing.T) []byte {
	t.Helper()
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	csrDER, err := buildJoinCSR(nodeKey, "spiffe://attacker.example/node/anything")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}
	return csrDER
}

func assertIssuedLeaf(t *testing.T, certPEM []byte) {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("JoinCSR returned no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://"+testTrustDomain+"/node/127.0.0.1:9100" {
		t.Fatalf("leaf URIs = %v, want exactly the token's bound identity", cert.URIs)
	}
}

// TestJoinServer_SignsAValidCSR_OverP256 is the FIPS join. The server runs with
// Noise unavailable, so it has no Noise key at all, and the client is given the
// P-256 key alone.
func TestJoinServer_SignsAValidCSR_OverP256(t *testing.T) {
	forceNoiseAvailable(t, false)
	js, _, token := startJoinServerForSuite(t)

	if len(js.PublicKey()) != 0 {
		t.Fatal("a join listener that may not use Noise must not hold a Noise key")
	}
	if got := len(js.P256PublicKey()); got != p256proto.PublicKeySize {
		t.Fatalf("P256PublicKey() = %d bytes, want %d", got, p256proto.PublicKeySize)
	}

	certPEM, err := JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerP256Key: js.P256PublicKey(),
		Token:         token,
	}, joinCSRFor(t), "spiffe://attacker.example/node/anything")
	if err != nil {
		t.Fatalf("JoinCSR over P-256: %v", err)
	}
	assertIssuedLeaf(t, certPEM)
}

// TestJoinServer_SignsAValidCSR_DualKeyClient covers
// the dual-key client against a listener serving both suites, in a runtime that
// may use Noise. Where it may not (a FIPS runtime) the same configuration must
// fall back to P-256, which is the other half of the ruling.
func TestJoinServer_SignsAValidCSR_DualKeyClient(t *testing.T) {
	js, _, token := startJoinServerForSuite(t)

	opts := &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerP256Key: js.P256PublicKey(),
		Token:         token,
	}
	if noiseAvailable() {
		opts.ServerKey = js.PublicKey()
	}
	certPEM, err := JoinCSR(context.Background(), opts, joinCSRFor(t), "spiffe://attacker.example/node/anything")
	if err != nil {
		t.Fatalf("JoinCSR: %v", err)
	}
	assertIssuedLeaf(t, certPEM)
}

// TestJoinCSR_DualKeyClientSelectsTheRuntimeSuite configures a joiner with
// both suites' keys and leaves the gating decision to the runtime, as a node
// started with both bootstrap flags does. Under crypto/fips140 the join must
// select the P-256 suite although a Noise key is configured, and complete.
// Under enforcement the runtime refuses X25519 itself, which the test asserts
// first, so a completed join is proof that X25519 was never reached.
func TestJoinCSR_DualKeyClientSelectsTheRuntimeSuite(t *testing.T) {
	js, _, token := startJoinServerForSuite(t)

	noiseKey := js.PublicKey()
	if !noiseAvailable() {
		// A FIPS-mode server serves no Noise key, so pin one it could never
		// answer: selecting Noise would then fail the join as well.
		noiseKey = make([]byte, 32)
	}
	opts := &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerKey:     noiseKey,
		ServerP256Key: js.P256PublicKey(),
		Token:         token,
	}

	wantSuite := p256proto.SuiteNoiseNK
	if fips140.Enabled() {
		wantSuite = p256proto.SuiteP256AESGCM
	}
	suite, _, err := selectJoinSuite(opts)
	if err != nil {
		t.Fatalf("selectJoinSuite: %v", err)
	}
	if suite != wantSuite {
		t.Fatalf("selectJoinSuite = %#x, want %#x (crypto/fips140.Enabled() = %v)", suite, wantSuite, fips140.Enabled())
	}

	if fips140.Enforced() {
		if _, err := ecdh.X25519().GenerateKey(rand.Reader); err == nil {
			t.Fatal("X25519 key generation succeeded under fips140=only: a completed join would not prove X25519 unreached")
		}
	}

	certPEM, err := JoinCSR(context.Background(), opts, joinCSRFor(t), "spiffe://attacker.example/node/anything")
	if err != nil {
		t.Fatalf("JoinCSR: %v", err)
	}
	assertIssuedLeaf(t, certPEM)
}

// TestJoinCSR_NoiseKeyOnlyIsRefusedUnderFIPS is the client half of "P-256 works
// where Noise is refused": the same client that succeeds above with a P-256 key
// is refused, before any dial, when the Noise key is all it has.
func TestJoinCSR_NoiseKeyOnlyIsRefusedUnderFIPS(t *testing.T) {
	forceNoiseAvailable(t, false)

	_, err := JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: "127.0.0.1:1",
		ServerKey:  make([]byte, 32),
	}, nil, "")
	var fipsErr *NoiseFIPSUnavailableError
	if !errors.As(err, &fipsErr) {
		t.Fatalf("error = %T (%v), want *NoiseFIPSUnavailableError", err, err)
	}
}

func TestJoinCSR_NoServerKeyIsRefused(t *testing.T) {
	_, err := JoinCSR(context.Background(), &JoinClientOptions{ServerAddr: "127.0.0.1:1"}, nil, "")
	var missing *MissingJoinServerKeyError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T (%v), want *MissingJoinServerKeyError", err, err)
	}
	if missing.Error() == "" {
		t.Error("MissingJoinServerKeyError has an empty message")
	}
}

func TestSelectJoinSuite(t *testing.T) {
	noiseKey := []byte{1}
	p256Key := []byte{2}

	cases := []struct {
		name      string
		available bool
		opts      JoinClientOptions
		suite     byte
		key       []byte
		errType   any
	}{
		{name: "noise only, available", available: true, opts: JoinClientOptions{ServerKey: noiseKey}, suite: p256proto.SuiteNoiseNK, key: noiseKey},
		{name: "p256 only, available", available: true, opts: JoinClientOptions{ServerP256Key: p256Key}, suite: p256proto.SuiteP256AESGCM, key: p256Key},
		{name: "both, available prefers noise", available: true, opts: JoinClientOptions{ServerKey: noiseKey, ServerP256Key: p256Key}, suite: p256proto.SuiteNoiseNK, key: noiseKey},
		{name: "both, FIPS requires p256", available: false, opts: JoinClientOptions{ServerKey: noiseKey, ServerP256Key: p256Key}, suite: p256proto.SuiteP256AESGCM, key: p256Key},
		{name: "p256 only, FIPS", available: false, opts: JoinClientOptions{ServerP256Key: p256Key}, suite: p256proto.SuiteP256AESGCM, key: p256Key},
		{name: "noise only, FIPS is refused", available: false, opts: JoinClientOptions{ServerKey: noiseKey}, errType: &NoiseFIPSUnavailableError{}},
		{name: "neither", available: true, opts: JoinClientOptions{}, errType: &MissingJoinServerKeyError{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forceNoiseAvailable(t, tc.available)
			suite, key, err := selectJoinSuite(&tc.opts)
			switch tc.errType.(type) {
			case *NoiseFIPSUnavailableError:
				var target *NoiseFIPSUnavailableError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want *NoiseFIPSUnavailableError", err)
				}
				return
			case *MissingJoinServerKeyError:
				var target *MissingJoinServerKeyError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v, want *MissingJoinServerKeyError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectJoinSuite: %v", err)
			}
			if suite != tc.suite || string(key) != string(tc.key) {
				t.Errorf("selectJoinSuite = (%#x, %v), want (%#x, %v)", suite, key, tc.suite, tc.key)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Refusals on the join listener's wire
// ---------------------------------------------------------------------------

// joinWireProbe sends one first frame to a listener and reports whether the
// server answered with a second handshake message. A server that refuses closes
// the connection without writing anything, which is what the callers assert.
func joinWireProbe(t *testing.T, addr string, first []byte) (answered bool) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	if err := noiseboot.WriteFrame(conn, first, deadline); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	_, err = noiseboot.ReadFrame(conn, deadline)
	return err == nil
}

func TestJoinServer_RefusesAMalformedFirstFrame(t *testing.T) {
	forceNoiseAvailable(t, false)
	js, _, _ := startJoinServerForSuite(t)

	cases := map[string][]byte{
		"empty":               {},
		"one byte":            {p256proto.Version},
		"unknown version":     {0x7f, p256proto.SuiteP256AESGCM, 1, 2, 3},
		"unknown suite":       {p256proto.Version, 0x7f, 1, 2, 3},
		"truncated p256 msg1": {p256proto.Version, p256proto.SuiteP256AESGCM, 1, 2, 3},
	}
	for name, first := range cases {
		t.Run(name, func(t *testing.T) {
			if joinWireProbe(t, js.Addr(), first) {
				t.Fatal("the join listener answered a first frame it must refuse")
			}
		})
	}
}

// TestJoinServer_NoiseSuiteRefusedWhereUnavailable is the listener half of the
// FIPS ruling: a Noise first frame reaches a listener that has no Noise key and
// is dropped without an answer, so a downgrade attempt does not even get a
// handshake message to analyse.
func TestJoinServer_NoiseSuiteRefusedWhereUnavailable(t *testing.T) {
	forceNoiseAvailable(t, false)
	js, _, _ := startJoinServerForSuite(t)

	first := append(noiseboot.NoiseSuiteHeader(), make([]byte, 32)...)
	if joinWireProbe(t, js.Addr(), first) {
		t.Fatal("a Noise first frame was answered by a listener that serves P-256 only")
	}
}

func TestJoinServer_P256GarbageAfterHandshakeDoesNotPanic(t *testing.T) {
	forceNoiseAvailable(t, false)
	js, _, _ := startJoinServerForSuite(t)

	conn, err := net.DialTimeout("tcp", js.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	completeP256JoinHandshake(t, conn, js.P256PublicKey(), deadline)
	if err := noiseboot.WriteFrame(conn, []byte("not a real ciphertext!!"), deadline); err != nil {
		t.Fatalf("WriteFrame (garbage ciphertext): %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	_, _ = conn.Read(buf) // expect EOF; must not hang or panic
}

// completeP256JoinHandshake drives the P-256 suite's handshake over conn as a
// raw initiator, leaving the connection at the point the server expects its
// first encrypted frame.
func completeP256JoinHandshake(t *testing.T, conn net.Conn, serverKey []byte, deadline time.Time) {
	t.Helper()
	ini, err := p256proto.NewInitiator(serverKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	defer ini.Destroy()
	msg1, err := ini.WriteMessage1()
	if err != nil {
		t.Fatalf("WriteMessage1: %v", err)
	}
	if err := noiseboot.WriteFrame(conn, msg1, deadline); err != nil {
		t.Fatalf("WriteFrame (msg1): %v", err)
	}
	if _, err := noiseboot.ReadFrame(conn, deadline); err != nil {
		t.Fatalf("ReadFrame (msg2): %v", err)
	}
}

func TestJoinSuiteUnavailableError_Message(t *testing.T) {
	if (&JoinSuiteUnavailableError{Suite: p256proto.SuiteNoiseNK}).Error() == "" {
		t.Error("JoinSuiteUnavailableError has an empty message")
	}
}
