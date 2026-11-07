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
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTAudit_SendsTheDocumentedRequest pins QueryAudit's and
// VerifyChain's verb and path on the wire, and the decoded answer.
func TestRESTAudit_SendsTheDocumentedRequest(t *testing.T) {
	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "QueryAudit/NoFilter", verb: http.MethodGet, path: "/v1/audit/events",
			answer: transport.AuditPage{Total: 1},
			callCheck: func(ctx context.Context, rt *Transport) error {
				page, err := rt.QueryAudit(ctx, transport.AuditFilter{})
				if err == nil && page.Total != 1 {
					return fmt.Errorf("QueryAudit total = %d, want 1", page.Total)
				}
				return err
			}},
		{name: "VerifyChain", verb: http.MethodGet, path: "/v1/audit/verify",
			answer: transport.AuditVerifyResult{Verified: true, Files: 2, Records: 10, Genesis: true},
			callCheck: func(ctx context.Context, rt *Transport) error {
				got, err := rt.VerifyChain(ctx)
				if err != nil {
					return err
				}
				if !got.Verified || got.Files != 2 || got.Records != 10 || !got.Genesis {
					return fmt.Errorf("VerifyChain = %+v, want verified over 2 files, 10 records, from genesis", *got)
				}
				return nil
			}},
	})
}

// TestRESTAudit_VerifyChainOutcomes checks how each answer from the verify
// endpoint reaches the caller: a broken chain is a verdict, not an error; a
// 501 is a NotImplementedError; any other status is a RequestError carrying
// it.
func TestRESTAudit_VerifyChainOutcomes(t *testing.T) {
	cases := map[string]struct {
		reply func(w http.ResponseWriter)
		check func(got *transport.AuditVerifyResult, err error) error
	}{
		"ChainBroken": {
			reply: func(w http.ResponseWriter) {
				writeJSON(w, &transport.AuditVerifyResult{Reason: "chain broken at index 3"})
			},
			check: func(got *transport.AuditVerifyResult, err error) error {
				if err != nil {
					return fmt.Errorf("want a verdict, got error %w", err)
				}
				if got.Verified || got.Reason != "chain broken at index 3" {
					return fmt.Errorf("verdict = %+v, want unverified with its reason", *got)
				}
				return nil
			},
		},
		"NotImplemented": {
			reply: func(w http.ResponseWriter) { http.Error(w, "not configured", http.StatusNotImplemented) },
			check: func(_ *transport.AuditVerifyResult, err error) error {
				var nie *transport.NotImplementedError
				if !errors.As(err, &nie) || nie.Method != "VerifyChain" {
					return fmt.Errorf("err = %v, want a NotImplementedError for VerifyChain", err)
				}
				return nil
			},
		},
		"ServerError": {
			reply: func(w http.ResponseWriter) { http.Error(w, "boom", http.StatusInternalServerError) },
			check: func(_ *transport.AuditVerifyResult, err error) error {
				var re *transport.RequestError
				var use *transport.UnexpectedStatusError
				if !errors.As(err, &re) || re.Method != "VerifyChain" ||
					!errors.As(err, &use) || use.StatusCode != http.StatusInternalServerError {
					return fmt.Errorf("err = %v, want a VerifyChain RequestError carrying status 500", err)
				}
				return nil
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			rs.handle(exactPath(http.MethodGet, "/v1/audit/verify"), func(w http.ResponseWriter, _ *http.Request) {
				c.reply(w)
			})
			if err := c.check(rt.VerifyChain(context.Background())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestRESTAudit_Closed checks the audit reads refuse on a closed transport
// before they reach the wire.
func TestRESTAudit_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"QueryAudit": func(ctx context.Context, rt *Transport) error {
			_, err := rt.QueryAudit(ctx, transport.AuditFilter{})
			return err
		},
		"VerifyChain": func(ctx context.Context, rt *Transport) error {
			_, err := rt.VerifyChain(ctx)
			return err
		},
	})
}
