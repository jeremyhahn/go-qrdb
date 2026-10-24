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
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// pollSignalProbe reports the real barrier's sealed state. A hold armed on it
// parks the next IsSealed call, before it reads that state, until the hold is
// released. With no request in flight the only caller is ServeSealedWindow's
// poll loop, so a hold armed between requests parks the loop: the listener
// cannot be torn down while it is held.
type pollSignalProbe struct {
	barrier *seal.StorageBarrier
	hold    atomic.Pointer[pollHold]
}

// pollHold is one armed hold: parked closes when a call takes it, and the
// call returns once release closes.
type pollHold struct {
	parked  chan struct{}
	release chan struct{}
}

func (p *pollSignalProbe) IsSealed() bool {
	if h := p.hold.Swap(nil); h != nil {
		close(h.parked)
		<-h.release
	}
	return p.barrier.IsSealed()
}

// TestServeSealedWindow_RefusesAnonymousRequestsAfterUnseal pins the
// unseal race on the boot-time sealed-window listener. That listener has no
// authentication middleware -- while sealed there is no identity plane to
// authenticate against -- so the only thing between an anonymous caller and
// every route registered on it is the sealed-window gate. The gate used to
// become a pass-through the moment the barrier unsealed, and the listener kept
// serving until the next poll noticed and Stop completed. In that window an
// anonymous DELETE /v1/barrier/recovery ran against an unsealed barrier and
// destroyed the recovery-key metadata.
//
// The listener must refuse every request once the barrier is unsealed, on the
// request itself rather than on the next poll. The test parks the poll loop
// so that request lands in the window deterministically, then releases it and
// checks the teardown: the unseal response closes its connection (RFC 9112
// §9.6), so a request after teardown fails at connect, never on a reused
// connection the teardown closed under it (main red 61439), and still never
// reaches the barrier.
func TestServeSealedWindow_RefusesAnonymousRequestsAfterUnseal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	secret := []byte("sealed-window-race-passphrase-32b")
	sw, err := seal.NewSoftwareStrategy(context.Background(), []byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase}}, sw)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	// This test needs recovery metadata to exist, so the recovery domain gets
	// three holders from the faithful in-memory distributor.
	distributor := sealtest.NewDistributor()
	distributor.SetHolders(seal.DomainRecovery, "r1", "r2", "r3")
	barrier.SetCeremonyDistributor(distributor)
	barrierSvc := service.NewBarrierService(barrier)
	// barrierSvc is built without a ceremony (NewBarrierService, not
	// NewBarrierServiceWithCeremony), so BarrierInitialize delegates
	// straight to the barrier rather than being refused (go-qrdb#548
	// refuses it only for a ceremony-backed BarrierService, qrdbd's own
	// system barrier shape); this test's barrierSvc has no such shape.
	if err := barrierSvc.BarrierInitialize(ctx, secret); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
	if _, err := barrierSvc.BarrierGenerateRecoveryKeys(ctx, 2, ""); err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if err := barrierSvc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	cfg := testConfig(t, true, false)
	tlsCfg := selfSignedTLSConfig(t)
	probe := &pollSignalProbe{barrier: barrier}

	serveCtx, stopServe := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- ServeSealedWindow(serveCtx, cfg, probe, WithTLS(tlsCfg), WithBarrierService(barrierSvc))
	}()
	served := sync.OnceValue(func() error { return <-done })
	t.Cleanup(func() {
		stopServe()
		_ = served()
	})

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg.Clone()}}
	t.Cleanup(client.CloseIdleConnections)
	base := "https://127.0.0.1:" + strconv.Itoa(cfg.Server.AdminPort)
	// do reports the status code and whether the response closed its
	// connection.
	do := func(method, path string, body []byte) (int, bool, error) {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return 0, false, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, false, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Close, nil
	}
	recoveryKeysSurvive := func(stage string, code int, err error) {
		t.Helper()
		has, hasErr := barrierSvc.BarrierHasRecoveryKeys(ctx)
		if hasErr != nil {
			t.Fatalf("BarrierHasRecoveryKeys: %v", hasErr)
		}
		if !has {
			t.Fatalf("an anonymous DELETE /v1/barrier/recovery %s reached the unsealed barrier through the sealed-window listener (code %d, err %v): recovery keys destroyed", stage, code, err)
		}
	}

	// Wait for the listener: a sealed node answers barrier status with its
	// initialized flag, so it is up once a status request is served.
	retry := time.NewTicker(20 * time.Millisecond)
	defer retry.Stop()
	for {
		code, _, err := do(http.MethodGet, "/v1/barrier/status", nil)
		if err == nil && code == http.StatusOK {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("sealed-window listener never answered: last code %d, err %v", code, err)
		case <-retry.C:
		}
	}

	// Park the poll loop: no request is in flight, so its next poll takes
	// the hold before it reads the sealed state.
	hold := &pollHold{parked: make(chan struct{}), release: make(chan struct{})}
	releaseHold := sync.OnceFunc(func() { close(hold.release) })
	t.Cleanup(releaseHold)
	probe.hold.Store(hold)
	select {
	case <-hold.parked:
	case <-ctx.Done():
		t.Fatal("the sealed-window poll loop never polled")
	}

	unseal, err := json.Marshal(map[string][]byte{"secret": secret})
	if err != nil {
		t.Fatalf("marshal unseal request: %v", err)
	}
	code, closed, err := do(http.MethodPost, "/v1/barrier/unseal", unseal)
	if err != nil || code/100 != 2 {
		t.Fatalf("anonymous unseal on the sealed-window listener: code %d, err %v", code, err)
	}
	if barrier.IsSealed() {
		t.Fatal("the unseal request did not unseal the barrier")
	}
	if !closed {
		t.Fatal("the unseal response kept its connection open; teardown can close it under the client's next request")
	}

	// In the window: the barrier is unsealed and the listener is still up.
	code, closed, err = do(http.MethodDelete, "/v1/barrier/recovery", nil)
	recoveryKeysSurvive("in the window", code, err)
	if err != nil {
		t.Fatalf("DELETE /v1/barrier/recovery: %v; want the listener to answer 503 itself", err)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE /v1/barrier/recovery after unseal: code %d, want %d", code, http.StatusServiceUnavailable)
	}
	if !closed {
		t.Fatal("the refusal after unseal kept its connection open; teardown can close it under the client's next request")
	}

	// Release the poll loop: it sees the unseal and tears the listener down.
	releaseHold()
	if err := served(); err != nil {
		t.Fatalf("ServeSealedWindow after unseal: %v", err)
	}
	code, _, err = do(http.MethodDelete, "/v1/barrier/recovery", nil)
	recoveryKeysSurvive("after teardown", code, err)
	if err == nil {
		t.Fatalf("DELETE /v1/barrier/recovery after teardown: code %d, want no listener to answer", code)
	}
}

// TestServeSealedWindow_StartsNoSQLListener: the SQL wire protocols carry no
// sealed-window verb, so the boot-time listener has nothing to serve on them
// and must not open them, while a regular server with the same config does.
func TestServeSealedWindow_StartsNoSQLListener(t *testing.T) {
	var started []string
	prevMySQL, prevPg := startMySQLFn, startPgSQLFn
	t.Cleanup(func() { startMySQLFn, startPgSQLFn = prevMySQL, prevPg })
	startMySQLFn = func(*Server) error { started = append(started, "mysql"); return nil }
	startPgSQLFn = func(*Server) error { started = append(started, "pgsql"); return nil }

	cfg := listenerTestConfig()
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true
	probe := &fakeSealedProbe{}

	listener := New(cfg, nil, nil, withSealedWindowListener(probe))
	if err := listener.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	listener.Stop()
	if len(started) != 0 {
		t.Fatalf("the sealed-window listener started %v", started)
	}

	regular := New(cfg, nil, nil, WithSealedWindowProbe(probe))
	if err := regular.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	regular.Stop()
	if len(started) != 2 {
		t.Fatalf("a regular server started %v, want mysql and pgsql", started)
	}
}

// TestSealedWindowOptions_LastWins: WithSealedWindowProbe and
// withSealedWindowListener select one gate; the last applied wins.
func TestSealedWindowOptions_LastWins(t *testing.T) {
	probe := &fakeSealedProbe{}
	var o serverOptions
	withSealedWindowListener(probe)(&o)
	if o.sealedWindowProbe == nil || !o.sealedWindowListener {
		t.Fatal("withSealedWindowListener did not select the listener gate")
	}
	WithSealedWindowProbe(probe)(&o)
	if o.sealedWindowListener {
		t.Fatal("WithSealedWindowProbe applied last must select the regular gate")
	}
}
