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

package embedded

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// UpdateNamespace forwards a non-replica refusal like every other vector
// method.
func TestForwardingScope_UpdateNamespaceForwards(t *testing.T) {
	fwd := &recordingForwarder{status: 200}
	fs := allFamiliesForwardingScope(t, fwd)
	_, _ = fs.UpdateNamespace(acmeCtx(), &transport.UpdateNamespaceRequest{})
	if len(fwd.calls) != 1 || fwd.calls[0].database != "orders" {
		t.Fatalf("forwarder calls = %+v; want one on orders", fwd.calls)
	}
}

// A verifiable-log call on a scope with no log service fails locally with
// the missing service named: that refusal is not a replica refusal, so the
// forwarder is never reached.
func TestForwardingScope_VlogWithoutServiceDoesNotForward(t *testing.T) {
	fwd := &recordingForwarder{status: 200}
	fs := allFamiliesForwardingScope(t, fwd)
	ctx := acmeCtx()
	cases := []struct {
		name string
		call func() error
	}{
		{"VlogAppend", func() error { _, err := fs.VlogAppend(ctx, &vlogwire.AppendRequest{}); return err }},
		{"VlogCreate", func() error { _, err := fs.VlogCreate(ctx, &vlogwire.CreateRequest{}); return err }},
		{"VlogExpireBodies", func() error { _, err := fs.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{}); return err }},
		{"VlogGet", func() error { _, err := fs.VlogGet(ctx, &vlogwire.GetRequest{}); return err }},
		{"VlogHead", func() error { _, err := fs.VlogHead(ctx, &vlogwire.LogRequest{}); return err }},
		{"VlogList", func() error { _, err := fs.VlogList(ctx, &vlogwire.ListRequest{}); return err }},
		{"VlogProveConsistency", func() error {
			_, err := fs.VlogProveConsistency(ctx, &vlogwire.ConsistencyRequest{})
			return err
		}},
		{"VlogProveInclusion", func() error { _, err := fs.VlogProveInclusion(ctx, &vlogwire.InclusionRequest{}); return err }},
		{"VlogRotateKey", func() error { _, err := fs.VlogRotateKey(ctx, &vlogwire.RotateKeyRequest{}); return err }},
		{"VlogSignHead", func() error { _, err := fs.VlogSignHead(ctx, &vlogwire.LogRequest{}); return err }},
	}
	for _, tc := range cases {
		err := tc.call()
		var nilSvc *NilServiceError
		if !errors.As(err, &nilSvc) || nilSvc.Service != "LogService" {
			t.Errorf("%s: err = %v; want the missing LogService", tc.name, err)
		}
	}
	if len(fwd.calls) != 0 {
		t.Fatalf("forwarder reached %d time(s); want none", len(fwd.calls))
	}
}

// Every refusal names what failed.
func TestEmbeddedErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&MissingAuditLoggerError{}, "audit logger is not configured"},
		{&NilContextBinderError{}, "context binder must not be nil"},
		{&NilBoundContextError{Method: "Get"}, "Get: context binder returned a nil context"},
		{&HolderActRateLimitedError{Operation: "ShareClaimBegin", RetryAfter: 2 * time.Second}, "ShareClaimBegin rate limited; retry after 2s"},
		{&UnregisteredOperationError{Method: "Frob"}, "Frob has no registered permission"},
	} {
		if msg := tc.err.Error(); !strings.Contains(msg, tc.want) {
			t.Errorf("%T.Error() = %q; want it to contain %q", tc.err, msg, tc.want)
		}
	}
}

// The replicated audit sink and seal store options set their fields.
func TestOptions_ReplicatedAuditAndSealStore(t *testing.T) {
	l := &service.OptInAuditLogger{}
	tr := &Transport{}
	WithReplicatedAuditLogger(l)(tr)
	WithSealStore(nil)(tr)
	if tr.replicatedAudit != l || tr.sealStoreSvc != nil {
		t.Fatalf("options not applied: %+v", tr)
	}
}
