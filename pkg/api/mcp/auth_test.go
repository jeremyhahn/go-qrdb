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

package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// replicaForwardingGap is the refusal the on-behalf verifier returns for a
// forward from a node across a release gap (rolling-upgrades clause (3)),
// wrapped the way the auth chain hands it back.
func replicaForwardingGap() (error, *release.GapError) {
	gap := &release.GapError{Channel: "replica-forwarding", Local: "0.59.0", Peer: "0.61.0"}
	return fmt.Errorf("on-behalf forward refused: %w", gap), gap
}

// TestWriteAuthenticationRefusal_ReleaseGapIs409 holds MCP to REST's answer
// for a replica forward across a release gap: 409 with the gap (channel and
// both releases) as the JSON error body, no bearer challenge, because the
// assertion verified and re-authenticating cannot cure a release gap.
func TestWriteAuthenticationRefusal_ReleaseGapIs409(t *testing.T) {
	err, gap := replicaForwardingGap()
	rec := httptest.NewRecorder()
	writeAuthenticationRefusal(rec, err, refusalTestChallenge)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := refusalBody(t, rec); got != gap.Error() {
		t.Errorf("error = %q, want %q", got, gap.Error())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q on a release gap", got)
	}
}

// TestAdmitWhenUnauthenticated_ReleaseGapIsNeverAnonymous proves a forward
// refused for a release gap is never served as an anonymous public tool call:
// the gap is a refusal, not a missing credential.
func TestAdmitWhenUnauthenticated_ReleaseGapIsNeverAnonymous(t *testing.T) {
	err, _ := replicaForwardingGap()
	if admitWhenUnauthenticated(nil, anonymousRequest{}, err) {
		t.Fatal("a release gap was admitted as an anonymous request")
	}
}

// TestAdmitAnonymous_ReleaseGapRefusesEvenAPublicToolCall proves the gap is
// refused before any anonymous admission rule runs, a public tool call
// included: a cluster serves nothing across a release gap.
func TestAdmitAnonymous_ReleaseGapRefusesEvenAPublicToolCall(t *testing.T) {
	err, _ := replicaForwardingGap()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"qrdb_health"}}`))
	perms := authmw.NewMCPToolRegistry()
	if rerr := perms.RegisterPublic("qrdb_health"); rerr != nil {
		t.Fatalf("RegisterPublic: %v", rerr)
	}
	s := &Server{toolPerms: perms}
	// The same call with no credential at all is admitted: the refusal
	// below is the gap's, not the registry's.
	if _, ok := s.admitAnonymous(httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"qrdb_health"}}`)), auth.ErrUnauthenticated); !ok {
		t.Fatal("the public tool call was not admitted anonymously")
	}
	if _, ok := s.admitAnonymous(r, err); ok {
		t.Fatal("a release gap was admitted as an anonymous public tool call")
	}
}
