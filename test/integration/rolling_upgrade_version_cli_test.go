//go:build integration

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

package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// SPEC row rolling-upgrades (#1628), engineering note Q1: a cluster runs
// only adjacent releases. Every qrdbd here is built stamped with a release
// through qrdbdharness.WithReleaseVersion, exactly as `make build` stamps one
// from VERSION.

// buildReleaseBinary builds qrdbd stamped as release v.
func buildReleaseBinary(t *testing.T, v string) string {
	t.Helper()
	bin, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage, qrdbdharness.WithReleaseVersion(v))
	if err != nil {
		t.Fatalf("build qrdbd %s: %v", v, err)
	}
	return bin
}

// TestRollingUpgrade_EnrolmentAcrossVersions_CLI: a joiner one minor release
// ahead of, or behind, both founders enrols, is admitted and replicates, and
// a `qrdb kv put` on a founder reads back from the joiner over every
// transport.
func TestRollingUpgrade_EnrolmentAcrossVersions_CLI(t *testing.T) {
	old := buildReleaseBinary(t, "0.58.0-alpha")
	next := buildReleaseBinary(t, "0.59.0-alpha")
	for _, row := range []struct {
		name           string
		founder, joins string
	}{
		{"newer joiner", old, next},
		{"older joiner", next, old},
	} {
		t.Run(row.name, func(t *testing.T) {
			c := startSelfIssuedCAFounders(t, [3]string{row.founder, row.founder, row.joins})
			c.startJoiner()
			c.admitJoiner()
			for _, proto := range multiProtocols {
				waitForCLIProtoReady(t, c.nodes[2], proto, 60*time.Second)
			}

			for _, proto := range multiProtocols {
				key := "rolling-upgrade-" + strings.ReplaceAll(row.name, " ", "-") + "-" + proto.name
				if out, err := runCLIProto(t, c.nodes[0], proto, "kv", "put", key, proto.name); err != nil {
					t.Fatalf("[%s] kv put on the founder: %v\noutput: %s", proto.name, err, out)
				}
				var last string
				if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
					out, err := runCLIProto(t, c.nodes[2], proto, "kv", "get", key)
					last = strings.TrimSpace(out)
					return err == nil && last == proto.name
				}) {
					t.Fatalf("[%s] kv get on the joiner = %q, want %q", proto.name, last, proto.name)
				}
			}
		})
	}
}

// TestRollingUpgrade_VersionGapRefused_CLI: a joiner two minor releases ahead
// of the founders is refused at enrolment with an error naming both
// releases, the founders still list exactly their two members over every
// transport, and a QUIC client offering only a frame protocol the founder
// does not speak is refused in the handshake with TLS alert 120.
func TestRollingUpgrade_VersionGapRefused_CLI(t *testing.T) {
	founder := buildReleaseBinary(t, "0.58.0-alpha")
	gap := buildReleaseBinary(t, "0.60.0-alpha")
	c := startSelfIssuedCAFounders(t, [3]string{founder, founder, gap})

	joinerLog := &logCapture{}
	c.startJoiner(joinerLog)
	refusal := regexp.MustCompile(`(release: enrollment: peer release "0\.58\.0-alpha" is not adjacent to local release "0\.60\.0-alpha")`)
	joinerLog.await(t, refusal, 60*time.Second)

	for _, proto := range multiProtocols {
		for _, id := range []uint64{1, 2} {
			if !adminPeersContainsReplica(t, c.nodes[0], proto, id) {
				t.Errorf("[%s] admin peers does not list founder replica %d", proto.name, id)
			}
		}
		if adminPeersContainsReplica(t, c.nodes[0], proto, 3) {
			t.Errorf("[%s] admin peers lists the refused joiner", proto.name)
		}
	}

	caPEM, err := os.ReadFile(c.caPath)
	if err != nil {
		t.Fatalf("read %s: %v", c.caPath, err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatalf("no certificate in %s", c.caPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, fmt.Sprintf("127.0.0.1:%d", c.listeners[0].quicPort), &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
		ServerName: "127.0.0.1",
		NextProtos: []string{"qrdb-quic/5"},
	}, &quicgo.Config{})
	if err == nil {
		_ = conn.CloseWithError(0, "")
		t.Fatal("QUIC dial offering only qrdb-quic/5 succeeded, want a no_application_protocol refusal")
	}
	var te *quicgo.TransportError
	if !errors.As(err, &te) || te.ErrorCode != quicgo.TransportErrorCode(0x178) {
		t.Fatalf("QUIC dial offering only qrdb-quic/5 = %v, want transport error 0x178 (TLS alert 120)", err)
	}
}

// TestRollingUpgrade_UnannouncedCandidateRefused_CLI: `qrdb admin member add`
// for a Raft address with no live advertisement -- a node that never
// enrolled -- is refused on every transport, and the address never joins.
// startSelfIssuedCAFounders takes a binary for every node, so the founder
// binary is passed three times and node 3 is never started.
func TestRollingUpgrade_UnannouncedCandidateRefused_CLI(t *testing.T) {
	founder := buildReleaseBinary(t, "0.59.0-alpha")
	c := startSelfIssuedCAFounders(t, [3]string{founder, founder, founder})

	for _, proto := range multiProtocols {
		out, err := runCLIProto(t, c.nodes[0], proto, "admin", "member", "add", c.listeners[2].raftAddr(),
			"--shard-id", "1", "--replica-id", "3")
		if err == nil {
			t.Fatalf("[%s] admin member add of an unannounced address succeeded: %s", proto.name, out)
		}
		if !strings.Contains(out, memberAddUnannouncedCandidateRefusal) {
			t.Fatalf("[%s] admin member add refusal = %q, want it to contain %q", proto.name, out, memberAddUnannouncedCandidateRefusal)
		}
		if adminPeersContainsReplica(t, c.nodes[0], proto, 3) {
			t.Errorf("[%s] admin peers lists the refused address", proto.name)
		}
	}
}
