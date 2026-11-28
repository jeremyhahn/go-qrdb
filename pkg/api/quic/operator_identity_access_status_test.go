package quic

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// An operator's access to a tenant that does not exist answers NotFound, as
// on REST, gRPC and MCP; a policy read that failed in the store answers
// Unavailable, so the client retries.
func TestServiceErrorStatus_OperatorAccessUnresolved(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want uint16
	}{
		{"missing tenant", &authmw.OperatorAccessUnresolvedError{TenantID: "ghost", Err: tenant.ErrTenantNotFound}, wire.StatusNotFound},
		{"store outage", &authmw.OperatorAccessUnresolvedError{TenantID: "acme", Err: errors.New("tenant store unavailable")}, wire.StatusUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServiceErrorStatus(tc.err); got != tc.want {
				t.Fatalf("ServiceErrorStatus = %d, want %d", got, tc.want)
			}
		})
	}
}
