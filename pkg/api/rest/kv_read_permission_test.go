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

package rest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// KV read permission is decided by the route's authorize step, before the
// service is reached. A caller holding the write and enumeration permissions
// but neither kv:get nor kv:range must be told 403 on every value-returning
// read, never handed the empty success a reached handler would produce from
// an empty answer. 403 is REST's typed denial; the SDK surfaces it as a
// RequestError carrying the status.

// grantOnly returns an authorizer that grants exactly perms.
func grantOnly(perms ...permission.Permission) *permAuthorizer {
	a := &permAuthorizer{granted: map[string]bool{}}
	for _, p := range perms {
		a.granted[permission.Resource(p)+":"+permission.Action(p)] = true
	}
	return a
}

func TestKVReads_WithoutGetOrRangeAreForbidden(t *testing.T) {
	authz := grantOnly(permission.KVPut, permission.KVList, permission.KVExists)
	srv := authedServer(t, authz)

	for name, target := range map[string]string{
		"get single key": "/kv/app/k1",
		"prefix scan":    "/kv/scan?prefix=app/",
		"full scan":      "/kv/scan",
		"range scan":     "/kv/range?start=app/a&end=app/z",
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveRaw(srv, http.MethodGet, target)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("GET %s = %d %s, want 403", target, rec.Code, rec.Body.String())
			}
			if body := rec.Body.String(); !strings.Contains(body, `"error"`) || strings.Contains(body, `"pairs"`) {
				t.Fatalf("GET %s body = %s, want an error and no result", target, body)
			}
		})
	}
}

// TestKVScan_ListAloneDoesNotReadValues pins the scan route to kv:range: a
// scan returns values, so kv:list (keys only) must not open it.
func TestKVScan_ListAloneDoesNotReadValues(t *testing.T) {
	authz := grantOnly(permission.KVList)
	srv := authedServer(t, authz)

	rec := serveRaw(srv, http.MethodGet, "/kv/scan?prefix=app/")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /kv/scan with kv:list only = %d, want 403", rec.Code)
	}

	// Control: kv:range is the permission that opens it.
	srv = authedServer(t, grantOnly(permission.KVRange))
	if rec := serveRaw(srv, http.MethodGet, "/kv/scan?prefix=app/"); rec.Code != http.StatusOK {
		t.Fatalf("GET /kv/scan with kv:range = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// TestKVList_ListAloneEnumeratesKeys pins the list route to kv:list, as on
// every other transport: listing returns keys, not values, so it needs
// neither kv:get nor kv:range.
func TestKVList_ListAloneEnumeratesKeys(t *testing.T) {
	srv := authedServer(t, grantOnly(permission.KVList))
	if rec := serveRaw(srv, http.MethodGet, "/kv?prefix=app/"); rec.Code != http.StatusOK {
		t.Fatalf("GET /kv with kv:list = %d %s, want 200", rec.Code, rec.Body.String())
	}

	srv = authedServer(t, grantOnly(permission.KVGet))
	if rec := serveRaw(srv, http.MethodGet, "/kv?prefix=app/"); rec.Code != http.StatusForbidden {
		t.Fatalf("GET /kv with kv:get only = %d, want 403", rec.Code)
	}
}

// TestKVGet_GrantedReachesTheHandler is the control for the denial above: the
// same caller holding kv:get is served.
func TestKVGet_GrantedReachesTheHandler(t *testing.T) {
	srv := authedServer(t, grantOnly(permission.KVGet))
	rec := serveRaw(srv, http.MethodGet, "/kv/app/k1")
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("GET /kv/app/k1 with kv:get = %d %s, want it authorized", rec.Code, rec.Body.String())
	}
}
