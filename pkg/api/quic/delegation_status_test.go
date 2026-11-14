package quic

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A delegation refused by the service, and a mint, renewal or delete that
// finds no standing policy, reach the client classified, not as Internal.
func TestMapServiceError_ClassifiesDelegationRefusals(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want uint16
	}{
		{"denied", &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}, wire.StatusPermissionDenied},
		{"no policy", &service.DelegationPolicyNotFoundError{TenantID: "acme", Principal: "billing"}, wire.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if status, _ := mapServiceError(tc.err); status != tc.want {
				t.Fatalf("status = %d, want %d", status, tc.want)
			}
		})
	}
}
