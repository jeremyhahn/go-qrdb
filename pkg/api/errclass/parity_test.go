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

package errclass_test

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// wrapped hides a sample behind one layer of wrapping, as every handler does,
// and gives it an Error method that cannot dereference a zero value's nil
// cause.
type wrapped struct{ inner error }

func (w wrapped) Error() string { return "wrapped sample" }
func (w wrapped) Unwrap() error { return w.inner }

// answers is what every wire transport says about one error, each read from
// that transport's REAL mapping — the function its handlers call — and never
// from a recording of it (go-qrdb#462). The unix socket serves the gRPC
// services and so answers gRPC's code; the embedded transport returns the
// typed error itself and has nothing to render.
type answers struct {
	rest int
	grpc codes.Code
	quic uint16
	mcp  int
}

func answer(err error) answers {
	return answers{
		rest: rest.ServiceErrorStatus(err),
		grpc: grpcapi.ServiceErrorCode(err),
		quic: quic.ServiceErrorStatus(err),
		mcp:  int(mcp.ServiceErrorCode(err)),
	}
}

// restStatusesByGRPCCode is the one piece of this test that is recorded
// rather than driven: which HTTP statuses MEAN each gRPC code. It is a
// relation between two public vocabularies, not an answer about any error, so
// no change to either transport's mapping can make it stale. FailedPrecondition
// and ResourceExhausted have several HTTP spellings because HTTP draws
// distinctions a client acts on (410 vs 412 vs 422, 413 vs 429) that gRPC does
// not; Canceled and DataLoss have none of their own and answer 500.
var restStatusesByGRPCCode = map[codes.Code][]int{
	codes.InvalidArgument:    {http.StatusBadRequest},
	codes.NotFound:           {http.StatusNotFound},
	codes.AlreadyExists:      {http.StatusConflict},
	codes.Aborted:            {http.StatusConflict},
	codes.FailedPrecondition: {http.StatusConflict, http.StatusGone, http.StatusPreconditionFailed, http.StatusUnprocessableEntity},
	codes.PermissionDenied:   {http.StatusForbidden},
	codes.Unauthenticated:    {http.StatusUnauthorized},
	codes.ResourceExhausted:  {http.StatusTooManyRequests, http.StatusRequestEntityTooLarge},
	codes.OutOfRange:         {http.StatusRequestedRangeNotSatisfiable},
	codes.Unimplemented:      {http.StatusNotImplemented},
	codes.Unavailable:        {http.StatusServiceUnavailable},
	codes.Canceled:           {http.StatusInternalServerError},
	codes.DataLoss:           {http.StatusInternalServerError},
	codes.Internal:           {http.StatusInternalServerError},
	codes.DeadlineExceeded:   {http.StatusGatewayTimeout},
}

// TestEveryTransportAgreesOnEveryCondition is the cross-transport parity test
// (go-qrdb#557). For every sample of every row of the classification table it
// asks each transport what it would answer, and fails when:
//
//   - QUIC's status is not the one QUIC gives gRPC's code (driven through
//     QUIC's own module-status path, which translates a gRPC status);
//   - MCP's code is not the one MCP gives gRPC's code (the same way);
//   - REST's status does not mean gRPC's code;
//   - two errors of the same class get different answers on any transport.
//
// A transport that answers any condition from its own clause instead of the
// table — the way three ladders drifted apart before — fails here, whichever
// side moved.
func TestEveryTransportAgreesOnEveryCondition(t *testing.T) {
	conditions := errclass.Conditions()
	if len(conditions) < 200 {
		t.Fatalf("only %d conditions: an empty loop reports success", len(conditions))
	}
	byClass := make(map[errclass.Class]answers)
	for _, c := range conditions {
		got := answer(wrapped{c.Sample})
		assertAgree(t, c.Row, got)
		if first, seen := byClass[c.Want]; seen && first != got {
			t.Errorf("%s: class %s answered %+v here and %+v for another row", c.Row, c.Want, got, first)
		}
		byClass[c.Want] = got
	}
}

// TestTransportLocalErrorsAgree drives each transport's own Classed refusals
// through every transport. Each type must declare EXACTLY the class listed
// here — a refusal that drifted to another class that also happens to agree
// across transports would otherwise pass — and every transport must render
// that class consistently.
func TestTransportLocalErrorsAgree(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want errclass.Class
	}{
		{"rest.ServiceNotImplementedError", &rest.ServiceNotImplementedError{Service: "mvcc"}, errclass.Unimplemented},
		{"rest.RequestTooLargeError", &rest.RequestTooLargeError{Limit: 1}, errclass.TooLarge},
		{"rest.PayloadTooLargeError", &rest.PayloadTooLargeError{Size: 2, Limit: 1}, errclass.TooLarge},
		{"rest.KeyTooLargeError", &rest.KeyTooLargeError{Size: 2, Limit: 1}, errclass.TooLarge},
		{"grpc.ServiceNotConfiguredError", &grpcapi.ServiceNotConfiguredError{Service: "backup"}, errclass.Unimplemented},
		{"grpc.CeremonyCANotConfiguredError", &grpcapi.CeremonyCANotConfiguredError{}, errclass.Unimplemented},
		{"grpc.MetadataUnmarshalError", &grpcapi.MetadataUnmarshalError{Err: http.ErrBodyNotAllowed}, errclass.InvalidArgument},
		{"grpc.MetadataMarshalError", &grpcapi.MetadataMarshalError{Err: http.ErrBodyNotAllowed}, errclass.Internal},
		{"grpc.VectorIDParseError", &grpcapi.VectorIDParseError{ID: "x", Err: http.ErrBodyNotAllowed}, errclass.InvalidArgument},
		{"grpc.TTLParseError", &grpcapi.TTLParseError{Value: "x", Err: http.ErrBodyNotAllowed}, errclass.InvalidArgument},
		{"grpc.DocumentUnmarshalError", &grpcapi.DocumentUnmarshalError{Err: http.ErrBodyNotAllowed}, errclass.InvalidArgument},
		{"grpc.DocumentMarshalError", &grpcapi.DocumentMarshalError{Err: http.ErrBodyNotAllowed}, errclass.Internal},
		{"grpc.SignatureDecodeError", &grpcapi.SignatureDecodeError{Err: http.ErrBodyNotAllowed}, errclass.InvalidArgument},
		{"grpc.TimestampParseError", &grpcapi.TimestampParseError{Field: "from_rfc3339", Value: "x", Err: http.ErrBodyNotAllowed}, errclass.InvalidArgument},
		{"quic.ServiceNotConfiguredError", &quic.ServiceNotConfiguredError{Service: "backup"}, errclass.Unimplemented},
		{"quic.CeremonyCANotConfiguredError", &quic.CeremonyCANotConfiguredError{}, errclass.Unimplemented},
		{"quic.InvalidArgumentError", &quic.InvalidArgumentError{Reason: "x"}, errclass.InvalidArgument},
		{"quic.UnmarshalError", &quic.UnmarshalError{Err: http.ErrBodyNotAllowed}, errclass.InvalidArgument},
		// BL2/N5: a *rbac.ResolveError wrapping a DEFINITIVE cause must render
		// the cause's own class on every transport, never the retryable
		// Unavailable a transient *rbac.ResolveError gets (authSamples,
		// pkg/api/errclass/table.go) -- see
		// TestClassify_ResolveErrorDefinitiveCausesAreNotUnavailable's doc
		// comment for why this case cannot live in authSamples itself.
		{"rbac.ResolveError wrapping a deleted or never-existent role", &rbac.ResolveError{Err: &role.RoleNotFoundError{ID: "deleted-role"}}, errclass.NotFound},
		{"rbac.ResolveError wrapping a malformed request", &rbac.ResolveError{Err: &role.ValidationError{Field: "id", Reason: "must not be empty"}}, errclass.InvalidArgument},
	} {
		if got := errclass.Classify(tc.err); got != tc.want {
			t.Errorf("%s declares class %s, want %s", tc.name, got, tc.want)
		}
		assertAgree(t, tc.name, answer(tc.err))
	}
}

// TestAuthRenderersAgreeWithTheTable: the auth middleware keeps its own
// renderers for its gates, which take a caller-chosen fallback. For every
// error it DOES classify, they must say what the table says.
func TestAuthRenderersAgreeWithTheTable(t *testing.T) {
	for _, c := range errclass.Conditions() {
		if c.Row != "middleware.ClassifyAuthError" {
			continue
		}
		s := wrapped{c.Sample}
		if got, want := authmw.HTTPStatus(s, 0), rest.ServiceErrorStatus(s); got != want {
			t.Errorf("%T: middleware.HTTPStatus = %d, REST = %d", c.Sample, got, want)
		}
		if got, want := status.Code(authmw.GRPCError(s, codes.Unknown)), grpcapi.ServiceErrorCode(s); got != want {
			t.Errorf("%T: middleware.GRPCError = %s, gRPC = %s", c.Sample, got, want)
		}
	}
}

func assertAgree(t *testing.T, row string, got answers) {
	t.Helper()
	viaGRPC := status.Error(got.grpc, "projected")
	if want := quic.ServiceErrorStatus(viaGRPC); got.quic != want {
		t.Errorf("%s: QUIC answers %d, but QUIC's status for gRPC %s is %d", row, got.quic, got.grpc, want)
	}
	if want := int(mcp.ServiceErrorCode(viaGRPC)); got.mcp != want {
		t.Errorf("%s: MCP answers %d, but MCP's code for gRPC %s is %d", row, got.mcp, got.grpc, want)
	}
	allowed, known := restStatusesByGRPCCode[got.grpc]
	if !known {
		t.Errorf("%s: gRPC answers %s, which no class renders", row, got.grpc)
		return
	}
	for _, s := range allowed {
		if got.rest == s {
			return
		}
	}
	t.Errorf("%s: REST answers %d, which does not mean gRPC's %s (allowed %v)", row, got.rest, got.grpc, allowed)
}
