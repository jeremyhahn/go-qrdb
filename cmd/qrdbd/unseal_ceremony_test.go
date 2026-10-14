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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// A sealed daemon is carried through the unseal ceremony to full startup.
//
// This is the one startup path a production node with seal enabled takes on
// every boot, and until now no test covered the HANDOVER: one test exercised
// the sealed window and stopped, another exercised first boot with seal off.
// Neither covered the transition between them, which is where a regression is
// least visible — the daemon comes up, serves the sealed window, and simply
// never proceeds (go-qrdb#96).
//
// The ceremony is driven the way an operator drives it: an HTTPS POST to
// /v1/ceremony/initialize against the sealed-window server -- "qrdb init" is
// the only system bootstrap (go-qrdb#548), and it brings the system barrier
// up itself in the same call rather than through the now-refused
// /v1/barrier/initialize. This daemon has auth disabled, but the seal
// subsystem -- and therefore the ceremony service -- is wired whenever
// seal.enabled is true regardless of auth.enabled, so the sealed-window
// listener still answers it. The test supplies the TLS material itself, so
// the client verifies the daemon's certificate against a CA it owns rather
// than skipping verification — a test that turns verification off proves
// nothing about a server that is meant to present a valid certificate.
//
// Readiness is taken from barrierUnsealed rather than from the admin port,
// because the port cannot distinguish the two servers: the sealed-window
// listener and the post-unseal server bind the SAME address, so a TCP dial is
// satisfied while the barrier is still shut.
func TestRunStart_UnsealCeremony_ProceedsToFullStartup(t *testing.T) {
	// TWO milestones, because they are different claims. barrierUnsealed says
	// the ceremony worked; serverServing says the handover completed. Between
	// them lie initializeNode, first boot, service construction and the server
	// bind, any of which can fail — so a test that stopped at the first would
	// prove the barrier opened and nothing about reaching a serving node,
	// while being named for the latter.
	unsealed := make(chan struct{}, 1)
	origUnsealed := barrierUnsealed
	t.Cleanup(func() { barrierUnsealed = origUnsealed })
	barrierUnsealed = func() {
		select {
		case unsealed <- struct{}{}:
		default:
		}
	}
	serving := servingHook(t)

	// The sealed-window listener accepting is the cue that the ceremony can be
	// performed -- it is NOT evidence of full startup, which is the confusion
	// this test exists to remove. So the attempt is ready only once the
	// ceremony has been accepted AND the regular server is serving: the
	// Noise bootstrap listener binds after the unseal, and a port another
	// process takes there ends the attempt as surely as one taken before
	// the sealed window, so startDaemon must still be able to start again.
	//
	// From here to the end of the test every exit is a t.Fatalf, and a Fatalf
	// ends this goroutine without reaching the shutdown below. startDaemon
	// registers the guard that stops the daemon on those paths -- the
	// initializeCeremony failure, the readiness timeout, and a runStart that
	// exits early -- so that a regression reports as this test failing rather
	// than as a daemon holding its ports and the process signal handlers
	// through everything that runs after it.
	var (
		dir         string
		wasUnsealed func() bool
	)
	done := startDaemon(t, func(p daemonPorts) daemonLaunch {
		dir = t.TempDir()
		// The daemon serves these, and the client below verifies against them.
		bundle := writeTLSFixtures(t, dir)

		// "pebble", the production default. This test exercises the
		// unseal-to-serving handover, which does not depend on which --sm
		// backs it.
		f := fullStartFlags(dir, p)
		f.enableGRPC = false
		f.sealConfig = qrdbconfig.SealConfig{
			// Enabled is the gate: without it runStart never brings up the
			// sealed window and the admin port is the ordinary server, which
			// is how the first draft of this test 404ed on the ceremony.
			Enabled:     true,
			Strategy:    "",
			RootKeyPath: "__sys:seal/root_key",
		}

		listening, isServing := adminReady(p), hookFired(serving)
		wasUnsealed = hookFired(unsealed)
		initialized := false
		return runStartLaunch(t, f, func() bool {
			if !initialized {
				if !listening() {
					return false
				}
				initializeCeremony(t, bundle, p.admin, "unseal-ceremony-passphrase")
				initialized = true
			}
			return isServing()
		})
	})

	// Serving is the second milestone; the first must have come before it,
	// or the node served without the handover this test is named for.
	if !wasUnsealed() {
		t.Fatal("the regular server is serving but the barrier never " +
			"reported the unseal: startup did not pass through awaitUnseal")
	}

	if runErr := signalUntilReturn(t, done, syscall.SIGTERM); runErr != nil {
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)

	// The cluster CA key (Key A) is confirmed persisted into the replicated
	// store during this same boot (confirmClusterCAPersisted, called once
	// waitForLeaderElection succeeds) and the local ca-key.pem fixture --
	// present on disk before runStart ever ran, exactly as a real first
	// node's bootstrap-generated file would be -- is deleted as soon as that
	// is confirmed. No CA private key file survives a completed boot.
	if _, err := os.Stat(filepath.Join(dir, "ca-key.pem")); !os.IsNotExist(err) {
		t.Fatalf("ca-key.pem still exists in the data directory after a completed boot (stat err = %v)", err)
	}
}

// initializeCeremony performs the operator's half of the ceremony: a single
// POST to /v1/ceremony/initialize that both brings the system barrier up
// (secret, the passphrase strategy's registered strategy expects) and runs
// the bootstrap-admin ceremony, exactly as "qrdb init" does (go-qrdb#548).
// This test's daemon has auth disabled and never looks at the admin
// credential the ceremony issues, so so_pin/user_pin are placeholders and
// admin_generate selects the dev-only generate path explicitly -- there is
// no credential-less default (go-qrdb#548 review) -- and its output is
// discarded.
func initializeCeremony(t *testing.T, bundle *TLSBundle, adminPort int, secret string) {
	t.Helper()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle.CACertPEM) {
		t.Fatal("the test's own CA PEM did not parse")
	}

	client := &http.Client{
		Timeout: readinessTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	body, err := json.Marshal(map[string]any{
		"so_pin":   "unseal-ceremony-so-pin",
		"user_pin": "unseal-ceremony-user-pin",
		// []byte, not the bare string: CeremonyInitializeRequest.Secret is
		// []byte (go-qrdb#560 review item 7), and encoding/json renders a
		// []byte as base64 on marshal -- the wire shape the REST decoder on
		// the other end expects. A bare string marshals as plain JSON
		// text instead, which is not valid base64 and the decoder rejects.
		"secret":         []byte(secret),
		"admin_generate": true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	url := fmt.Sprintf("https://127.0.0.1:%d/v1/ceremony/initialize", adminPort)

	// The listener accepts before the handler is necessarily wired, so this
	// retries until the endpoint answers rather than betting on one attempt.
	deadline := time.Now().Add(readinessTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		req, rerr := http.NewRequestWithContext(context.Background(),
			http.MethodPost, url, bytes.NewReader(body))
		if rerr != nil {
			t.Fatalf("new request: %v", rerr)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, derr := client.Do(req)
		if derr != nil {
			lastErr = derr
			time.Sleep(signalRetryInterval)
			continue
		}
		_ = resp.Body.Close()

		if resp.StatusCode < 300 {
			return
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		time.Sleep(signalRetryInterval)
	}
	t.Fatalf("ceremony initialize never succeeded within %v: %v", readinessTimeout, lastErr)
}
