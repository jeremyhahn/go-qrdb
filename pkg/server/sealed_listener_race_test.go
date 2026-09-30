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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// pollSignalProbe reports the real barrier's sealed state and signals every
// call on polled. Before the test sends its first request the only caller is
// ServeSealedWindow's poll loop, so a receive on polled marks the start of a
// poll interval.
type pollSignalProbe struct {
	barrier *seal.StorageBarrier
	polled  chan struct{}
}

func (p *pollSignalProbe) IsSealed() bool {
	select {
	case p.polled <- struct{}{}:
	default:
	}
	return p.barrier.IsSealed()
}

// recoverySink is a seal.CeremonyDistributor that accepts every share and
// seals it nowhere: this test needs recovery metadata to exist, not officers.
type recoverySink struct{}

func (recoverySink) SealAndDistribute(_ context.Context, _ []byte, _, total int) (int, error) {
	return total, nil
}

func (recoverySink) RekeyAndDistribute(_ context.Context, _ []byte, _, total int) (int, error) {
	return total, nil
}

var _ seal.CeremonyDistributor = recoverySink{}

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
// request itself rather than on the next poll.
func TestServeSealedWindow_RefusesAnonymousRequestsAfterUnseal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	secret := []byte("sealed-window-race-passphrase-32b")
	sw, err := seal.NewSoftwareStrategy([]byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase}}, sw)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	barrier.SetRecoveryDistributor(recoverySink{})
	barrierSvc := service.NewBarrierService(barrier)
	if err := barrierSvc.BarrierInitialize(ctx, secret); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
	if _, err := barrierSvc.BarrierGenerateRecoveryKeys(ctx, 2, 3); err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if err := barrierSvc.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	cfg := testConfig(t, true, false)
	tlsCfg := selfSignedTLSConfig(t)
	probe := &pollSignalProbe{barrier: barrier, polled: make(chan struct{}, 1)}

	serveCtx, stopServe := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- ServeSealedWindow(serveCtx, cfg, probe, WithTLS(tlsCfg), WithBarrierService(barrierSvc))
	}()
	t.Cleanup(func() {
		stopServe()
		<-done
	})

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg.Clone()}}
	t.Cleanup(client.CloseIdleConnections)
	base := "https://127.0.0.1:" + strconv.Itoa(cfg.Server.AdminPort)
	do := func(method, path string, body []byte) (int, error) {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	// Wait for the listener, over the anonymous sealed-window status verb.
	retry := time.NewTicker(20 * time.Millisecond)
	defer retry.Stop()
	for {
		code, err := do(http.MethodGet, "/v1/barrier/status", nil)
		if err == nil && code == http.StatusOK {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("sealed-window listener never answered: last code %d, err %v", code, err)
		case <-retry.C:
		}
	}

	// Align with the poll loop: drain the signal the status requests left,
	// then wait for the loop's own next poll. The next one is a full poll
	// interval away, which is the window the operator's unseal opens.
	select {
	case <-probe.polled:
	default:
	}
	select {
	case <-probe.polled:
	case <-ctx.Done():
		t.Fatal("the sealed-window poll loop never polled")
	}

	unseal, err := json.Marshal(map[string][]byte{"secret": secret})
	if err != nil {
		t.Fatalf("marshal unseal request: %v", err)
	}
	if code, err := do(http.MethodPost, "/v1/barrier/unseal", unseal); err != nil || code/100 != 2 {
		t.Fatalf("anonymous unseal on the sealed-window listener: code %d, err %v", code, err)
	}
	if barrier.IsSealed() {
		t.Fatal("the unseal request did not unseal the barrier")
	}

	code, err := do(http.MethodDelete, "/v1/barrier/recovery", nil)
	has, hasErr := barrierSvc.BarrierHasRecoveryKeys(ctx)
	if hasErr != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", hasErr)
	}
	if !has {
		t.Fatalf("an anonymous DELETE /v1/barrier/recovery reached the unsealed barrier through the sealed-window listener (code %d, err %v): recovery keys destroyed", code, err)
	}
	if err != nil {
		t.Fatalf("DELETE /v1/barrier/recovery: %v; want the listener to answer 503 itself", err)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE /v1/barrier/recovery after unseal: code %d, want %d", code, http.StatusServiceUnavailable)
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
	if got := sealedWindowGate(true, "gate", "closed"); got != "closed" {
		t.Errorf("sealedWindowGate(true) = %q, want closed", got)
	}
	if got := sealedWindowGate(false, "gate", "closed"); got != "gate" {
		t.Errorf("sealedWindowGate(false) = %q, want gate", got)
	}
}
