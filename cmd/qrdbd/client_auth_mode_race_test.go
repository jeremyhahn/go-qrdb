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
	"context"
	"crypto/tls"
	"net"
	"sync"
	"testing"
	"time"
)

// TestSetClientAuthMode_RaceFreeAgainstConcurrentHandshakes is go-qrdb#591's
// review, item 3's proof: real TLS handshakes against a listener sharing
// bundle.TLSConfig -- standing in for the pre-host sealed-window listener,
// which shares this exact pointer -- run CONCURRENTLY with repeated
// SetClientAuthMode calls, standing in for the boot sequence installing the
// auth-derived default right after the sealed-window listener's own Stop
// returns (which this test's own earlier investigation confirmed can
// precede an in-flight handshake's own completion: pkg/api/rest.Server.Stop
// bounds http.Server.Shutdown to a fixed 5-second timeout, so Stop can
// return with a slow connection's handshake still running against the
// exact TLSConfig pointer a later boot phase is about to reconfigure).
//
// Before this fix, SetClientAuthMode's callers mutated
// bundle.TLSConfig.ClientAuth/.ClientCAs directly -- plain field writes,
// unsynchronized against configForClient's own plain field READS of the
// very same fields on every handshake -- which -race reliably reports as a
// DATA RACE under this exact concurrent access pattern. Run with -race.
func TestSetClientAuthMode_RaceFreeAgainstConcurrentHandshakes(t *testing.T) {
	bundle := testBundle(t)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", bundle.TLSConfig)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Accept loop: every incoming connection just has its handshake
	// attempted and is then dropped. The outcome (succeeds, or fails
	// because the flipping ClientAuth mode happened to require a
	// certificate this test's bare client never presents) is irrelevant;
	// only race-freedom is under test here.
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
			}(c)
		}
	}()

	// Flips the applied mode continuously, standing in for the boot
	// sequence's own repeated (in practice, one-shot, but repeated here to
	// maximize contention) SetClientAuthMode call racing live handshakes.
	stop := make(chan struct{})
	flipDone := make(chan struct{})
	go func() {
		defer close(flipDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			bundle.SetClientAuthMode(tls.NoClientCert, nil)
			bundle.SetClientAuthMode(tls.VerifyClientCertIfGiven, bundle.ClientCACertPool)
		}
	}()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			conn, derr := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
			if derr != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = conn.HandshakeContext(ctx)
		}()
	}
	wg.Wait()

	close(stop)
	select {
	case <-flipDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the SetClientAuthMode flipping goroutine did not stop")
	}
}
