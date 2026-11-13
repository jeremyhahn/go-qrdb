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

//go:build integration

package integration_test

import (
	"strings"
	"testing"
)

// TestPublicHealth_CLI_NoCertificate_PerProtocol drives the qrdb binary
// against a real qrdbd on every transport with no client certificate:
// `admin health`, a publicverbs.Table verb, succeeds, and `admin status`,
// which needs an identity, is refused. The same `admin status` with the
// operator's certificate is the control that the command exists and is
// served, so its refusal is the missing certificate's alone.
func TestPublicHealth_CLI_NoCertificate_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range invitationProtocols {
		t.Run(proto.name, func(t *testing.T) {
			anon := unauthenticatedArgsFor(node, proto)

			out, err := runCLI(t, node.cli, append(append([]string{}, anon...), "admin", "health")...)
			if err != nil {
				t.Fatalf("[%s] admin health with no certificate: %v\noutput: %s", proto.name, err, out)
			}

			if out, err := runCLIProto(t, node, proto, "admin", "status"); err != nil {
				t.Fatalf("[%s] admin status with a certificate (control): %v\noutput: %s", proto.name, err, out)
			}

			out, err = runCLI(t, node.cli, append(append([]string{}, anon...), "admin", "status")...)
			if err == nil {
				t.Fatalf("[%s] admin status with no certificate succeeded, want a refusal: %s", proto.name, out)
			}
			if strings.Contains(strings.ToLower(out), "unknown command") {
				t.Fatalf("[%s] admin status is not a command: %s", proto.name, out)
			}
		})
	}
}
