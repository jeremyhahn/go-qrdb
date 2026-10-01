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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// memCredentials is an in-memory ceremony.CredentialServicer.
type memCredentials struct {
	mu    sync.Mutex
	creds map[string][]byte
}

func (m *memCredentials) StoreCredential(_ context.Context, name string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.creds == nil {
		m.creds = map[string][]byte{}
	}
	m.creds[name] = append([]byte(nil), value...)
	return nil
}

var _ ceremony.CredentialServicer = (*memCredentials)(nil)

// enrollmentFixture is a boot-time sealed-window listener over a real
// barrier and ceremony core, answering on REST.
type enrollmentFixture struct {
	barrier *seal.StorageBarrier
	core    *ceremony.CeremonyService
	client  *http.Client
	base    string
}

// startEnrollmentListener boots the sealed-window listener over a barrier
// that prepare puts in the state under test, with a fresh ceremony core -- the
// state a restarted daemon is in, since ceremony state is in memory.
func startEnrollmentListener(t *testing.T, prepare func(ctx context.Context, b *seal.StorageBarrier)) *enrollmentFixture {
	t.Helper()
	return startEnrollmentListenerOn(t, seal.NewMemoryBackend(), prepare)
}

// startEnrollmentListenerOn is startEnrollmentListener over base.
func startEnrollmentListenerOn(t *testing.T, base seal.StorageBackend, prepare func(ctx context.Context, b *seal.StorageBarrier)) *enrollmentFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sw, err := seal.NewSoftwareStrategy([]byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(logger, base,
		seal.BarrierConfig{RootKeyPath: "root-key", PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase}}, sw)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	prepare(ctx, barrier)
	if !barrier.IsSealed() {
		t.Fatal("prepare must leave the barrier sealed")
	}

	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, &memCredentials{}, logger)
	if err != nil {
		t.Fatalf("NewCeremonyService: %v", err)
	}
	core.SetCA(testServerCA)

	cfg := testConfig(t, true, false)
	tlsCfg := selfSignedTLSConfig(t)
	serveCtx, stopServe := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- ServeSealedWindow(serveCtx, cfg, barrier, WithTLS(tlsCfg),
			WithCeremonyService(service.NewCeremonyService(core)),
			WithBarrierService(service.NewBarrierServiceWithCeremony(barrier, core)))
	}()
	t.Cleanup(func() {
		stopServe()
		<-done
	})

	f := &enrollmentFixture{
		barrier: barrier,
		core:    core,
		client:  &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg.Clone()}},
		base:    "https://127.0.0.1:" + strconv.Itoa(cfg.Server.AdminPort),
	}
	t.Cleanup(f.client.CloseIdleConnections)

	retry := time.NewTicker(20 * time.Millisecond)
	defer retry.Stop()
	for {
		code, _, err := f.do(ctx, http.MethodGet, "/v1/barrier/status", nil)
		if err == nil && code == http.StatusOK {
			return f
		}
		select {
		case <-ctx.Done():
			t.Fatalf("sealed-window listener never answered: code %d, err %v", code, err)
		case <-retry.C:
		}
	}
}

func (f *enrollmentFixture) do(ctx context.Context, method, path string, body []byte) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, f.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out), nil
}

// registerAnonymously submits an unseal-officer registration with no
// credentials.
func (f *enrollmentFixture) registerAnonymously(t *testing.T, username string) (int, string) {
	t.Helper()
	return f.registerAnonymouslyAt(t, "/v1/barrier/officers", username)
}

// registerAnonymouslyAt submits an officer registration to path with no
// credentials.
func (f *enrollmentFixture) registerAnonymouslyAt(t *testing.T, path, username string) (int, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.CreateCertificateRequest(crand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: username}}, key)
	if err != nil {
		t.Fatalf("CreateCertificateRequest: %v", err)
	}
	body, err := json.Marshal(map[string]string{
		"username": username,
		"csr_pem":  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, out, err := f.do(ctx, http.MethodPost, path, body)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return code, out
}

// TestSealedWindow_OfficerEnrollmentClosedAfterInitialization pins the
// PreOperationalOnly exemption. Officer registration CA-signs any CSR it is
// given and assigns a share index, and on the boot-time sealed-window listener
// it is anonymous. That is justified only while the enrollment ceremony is
// still open. A daemon restarted while sealed after its barrier was
// initialized starts with a fresh, pre-operational ceremony in memory, and the
// listener used to go on enrolling anyone who asked -- handing an anonymous
// caller a certificate signed by the cluster CA.
func TestSealedWindow_OfficerEnrollmentClosedAfterInitialization(t *testing.T) {
	f := startEnrollmentListener(t, func(ctx context.Context, b *seal.StorageBarrier) {
		if err := service.NewBarrierService(b).BarrierInitialize(ctx, []byte("sealed-window-enrollment-pass")); err != nil {
			t.Fatalf("BarrierInitialize: %v", err)
		}
		if err := b.Seal(); err != nil {
			t.Fatalf("Seal: %v", err)
		}
	})

	code, out := f.registerAnonymously(t, "mallory")
	if strings.Contains(out, "BEGIN CERTIFICATE") {
		t.Fatalf("an anonymous caller enrolled an officer after initialization and was issued a CA-signed certificate (status %d)", code)
	}
	if code != http.StatusForbidden || !strings.Contains(out, `"ENROLLMENT_CLOSED"`) {
		t.Fatalf("anonymous officer registration after initialization: status %d, body %s; want 403 ENROLLMENT_CLOSED", code, out)
	}
}

// TestSealedWindow_OfficerEnrollmentClosedByOperationalMarker: a ceremony
// that reached operational persists a marker the sealed window reads, so the
// exemption stays closed across a restart whatever the barrier's state.
func TestSealedWindow_OfficerEnrollmentClosedByOperationalMarker(t *testing.T) {
	f := startEnrollmentListener(t, func(ctx context.Context, b *seal.StorageBarrier) {
		if err := b.MarkCeremonyOperational(ctx); err != nil {
			t.Fatalf("MarkCeremonyOperational: %v", err)
		}
	})

	code, out := f.registerAnonymously(t, "mallory")
	if code != http.StatusForbidden || !strings.Contains(out, `"ENROLLMENT_CLOSED"`) {
		t.Fatalf("anonymous officer registration with the operational marker set: status %d, body %s; want 403 ENROLLMENT_CLOSED", code, out)
	}
}

// TestSealedWindow_OfficerEnrollmentOpenBeforeInitialization: first boot
// still works -- officers register anonymously on the sealed-window listener
// before the barrier is initialized, into the unseal domain and the recovery
// domain alike. seal:officer_register is ScopeCross, so on an authenticated
// listener only a system-tenant identity may register; this listener has no
// authentication middleware and runs no scope rule, so the pre-operational
// exemption (sealedwindow.PreOperationalOnly) is what admits the anonymous
// first-boot officer, and the scope change must not reach it.
func TestSealedWindow_OfficerEnrollmentOpenBeforeInitialization(t *testing.T) {
	for _, path := range []string{"/v1/barrier/officers", "/v1/barrier/recovery/officers"} {
		t.Run(path, func(t *testing.T) {
			f := startEnrollmentListener(t, func(context.Context, *seal.StorageBarrier) {})

			code, out := f.registerAnonymouslyAt(t, path, "alice")
			if code != http.StatusOK || !strings.Contains(out, "BEGIN CERTIFICATE") {
				t.Fatalf("first-boot officer registration: status %d, body %s; want 200 with a certificate", code, out)
			}
		})
	}
}

// TestSealedWindow_AnonymousClaimStatusIsRedacted: the first-boot wizard
// tracks claims through officer-claim-status, which is anonymous on the
// boot-time listener. It must say how many officers there are and which share
// indexes are claimed, and nothing that identifies them: it used to return
// every officer's username, certificate thumbprint and auth factor to anyone.
func TestSealedWindow_AnonymousClaimStatusIsRedacted(t *testing.T) {
	f := startEnrollmentListener(t, func(context.Context, *seal.StorageBarrier) {})
	if code, out := f.registerAnonymously(t, "alice-officer"); code != http.StatusOK {
		t.Fatalf("register: status %d, body %s", code, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, out, err := f.do(ctx, http.MethodGet, "/v1/barrier/officers/claim-status", nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("claim-status: status %d, err %v, body %s", code, err, out)
	}
	if strings.Contains(out, "alice-officer") {
		t.Fatalf("anonymous claim-status disclosed an officer username: %s", out)
	}
	var resp struct {
		Officers []map[string]any `json:"officers"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	statuses := resp.Officers
	if len(statuses) != 1 {
		t.Fatalf("claim-status returned %d officers, want 1: %s", len(statuses), out)
	}
	if idx, _ := statuses[0]["share_index"].(float64); idx != 1 {
		t.Errorf("share_index = %v, want 1: %s", statuses[0]["share_index"], out)
	}
	for _, field := range []string{"cert_thumbprint", "auth_factor"} {
		if v, _ := statuses[0][field].(string); v != "" {
			t.Errorf("anonymous claim-status disclosed %s = %q", field, v)
		}
	}
}

// stallingMarkerBackend blocks every read of the ceremony-operational marker
// until the reader's context ends: a seal store that has stopped answering.
type stallingMarkerBackend struct {
	seal.StorageBackend
}

func (b *stallingMarkerBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.HasSuffix(key, ".ceremony-operational") {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.StorageBackend.Get(ctx, key)
}

// TestSealedWindow_EnrollmentProbeIsBounded: the listener asks the barrier
// whether enrollment is open on every registration. That read must be bounded
// -- by the request and a short deadline -- and fail closed when it runs out.
// It used to run under context.Background(), so a seal store that stopped
// answering held the request, and the listener's admission, forever.
func TestSealedWindow_EnrollmentProbeIsBounded(t *testing.T) {
	f := startEnrollmentListenerOn(t, &stallingMarkerBackend{StorageBackend: seal.NewMemoryBackend()},
		func(context.Context, *seal.StorageBarrier) {})

	type result struct {
		code int
		out  string
	}
	done := make(chan result, 1)
	go func() {
		code, out := f.registerAnonymously(t, "alice")
		done <- result{code, out}
	}()
	select {
	case r := <-done:
		if r.code != http.StatusForbidden || !strings.Contains(r.out, `"ENROLLMENT_CLOSED"`) {
			t.Fatalf("registration with an unreadable marker: status %d, body %s; want 403 ENROLLMENT_CLOSED (fail closed)", r.code, r.out)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the enrollment probe never returned: it is not bounded")
	}
}
