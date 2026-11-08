package mcp

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A delegation refused by the service, and a mint, renewal or delete that
// finds no standing policy, reach the client classified, not as Internal.
func TestServiceErrorCode_DelegationRefusals(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want wire.Code
	}{
		{"denied", &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}, wire.CodePermissionDenied},
		{"no policy", &service.DelegationPolicyNotFoundError{TenantID: "acme", Principal: "billing"}, wire.CodeNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if code := ServiceErrorCode(tc.err); code != tc.want {
				t.Fatalf("code = %v, want %v", code, tc.want)
			}
		})
	}
}
