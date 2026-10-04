// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package integration_test

// The replica-forwarding suite's REST client.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type restFwdTransport struct{}

func (restFwdTransport) name() string { return "rest" }

// do sends one request to node i as caller, scoped to db.
func (restFwdTransport) do(t *testing.T, c *fwdCluster, i int, caller fwdCaller, method, path, db, body string) fwdAnswer {
	t.Helper()
	certPath, keyPath := c.certFor(caller)
	client := mtlsHTTPClientFromFiles(t, c.ca, certPath, keyPath, 30*time.Second)
	req, err := http.NewRequest(method, fmt.Sprintf("https://127.0.0.1:%d%s", c.nodes[i].admin, path), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-QRDB-Database", db)
	resp, err := client.Do(req)
	if err != nil {
		return fwdAnswer{detail: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	a := fwdAnswer{
		ok:       resp.StatusCode/100 == 2,
		value:    string(raw),
		servedBy: resp.Header.Get("X-Qrdb-Forwarded-To"),
		denied:   resp.StatusCode == http.StatusForbidden,
		notFound: resp.StatusCode == http.StatusNotFound,
		detail:   fmt.Sprintf("%d %s", resp.StatusCode, raw),
	}
	var e struct {
		Code     string            `json:"code"`
		Metadata map[string]string `json:"metadata"`
	}
	if resp.StatusCode == http.StatusServiceUnavailable && json.Unmarshal(raw, &e) == nil && e.Code == "NOT_A_REPLICA" {
		a.refusal = e.Metadata
	}
	return a
}

func (r restFwdTransport) put(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key, value string) fwdAnswer {
	t.Helper()
	return r.do(t, c, node, caller, http.MethodPut, "/kv/"+key, db, value)
}

func (r restFwdTransport) get(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key string) fwdAnswer {
	t.Helper()
	return r.do(t, c, node, caller, http.MethodGet, "/kv/"+key, db, "")
}

func (r restFwdTransport) watch(t *testing.T, c *fwdCluster, node int, db string) fwdAnswer {
	t.Helper()
	return r.do(t, c, node, fwdAsWriter, http.MethodGet, "/watch?key=k", db, "")
}
