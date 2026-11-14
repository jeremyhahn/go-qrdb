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

package clusterkeys_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// A structural Open failure is the sender's fault (400), wrapped or not;
// every other Open failure is one opaque 403.
func TestHTTPCodeForOpenErr(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{keydelivery.ErrMalformedEnvelope, http.StatusBadRequest},
		{fmt.Errorf("open: %w", keydelivery.ErrInvalidMeta), http.StatusBadRequest},
		{keydelivery.ErrInvalidMode, http.StatusBadRequest},
		{errors.New("decrypt failed"), http.StatusForbidden},
	} {
		if got := clusterkeys.HTTPCodeForOpenErr(tc.err); got != tc.want {
			t.Errorf("httpCodeForOpenErr(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// A delivery whose mode has an importer runs it: success and a key already
// installed (an idempotent retry) answer 200 imported; any other import
// failure answers 500 and imports nothing.
func TestHandler_ModeDispatch(t *testing.T) {
	ca := newTestCA(t)
	follower := newTestNodeCert(t, ca, "cluster.local", "follower-node-1")
	p := newTestPlacement("tenant-1", "db-1", 1, 2, "follower-node-1", "")
	recv := newMinimalReceiver(t, follower, nil, &staticPlacementGetter{p: p})
	meta := keydelivery.Meta{
		TenantID:     "tenant-1",
		DatabaseID:   "db-1",
		ShardID:      1,
		ReplicaID:    2,
		TargetNodeID: "follower-node-1",
		Mode:         testUnknownMode,
	}

	for _, tc := range []struct {
		name     string
		importer error
		want     int
		body     string
	}{
		{"imported", nil, http.StatusOK, `"imported":true`},
		{"already initialized", seal.ErrAlreadyInitialized, http.StatusOK, `"imported":true`},
		{"tenant already exists", fmt.Errorf("import: %w", seal.ErrTenantAlreadyExists), http.StatusOK, `"imported":true`},
		{"import failed", errors.New("barrier refused"), http.StatusInternalServerError, "key import failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []byte
			clusterkeys.SetModeImporterForTest(t, testUnknownMode, func(_ context.Context, m keydelivery.Meta, plaintext []byte) error {
				if m.TenantID != "tenant-1" {
					t.Errorf("importer meta tenant = %q", m.TenantID)
				}
				got = append([]byte(nil), plaintext...)
				return tc.importer
			})

			env, err := keydelivery.Seal(&follower.key.PublicKey, meta, []byte("tenant-key-material"))
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			raw, _ := json.Marshal(env)
			rec := httptest.NewRecorder()
			recv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", bytes.NewReader(raw)))
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("status %d body %q; want %d containing %q", rec.Code, rec.Body.String(), tc.want, tc.body)
			}
			if string(got) != "tenant-key-material" {
				t.Fatalf("importer plaintext = %q", got)
			}
		})
	}
}
