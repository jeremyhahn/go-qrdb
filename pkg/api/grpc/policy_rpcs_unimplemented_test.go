package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// admittingAuthenticator authenticates every call as one tenant user.
type admittingAuthenticator struct{}

func (admittingAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	return &auth.Identity{Subject: "alice", TenantID: "acme"}, nil
}

func (admittingAuthenticator) Name() string { return "admitting" }

// allowAllAuthorizer grants every permission, so a call reaches the service
// handler and the answer is the handler's own.
type allowAllAuthorizer struct{}

func (allowAllAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return nil
}

// TestQRDBService_PolicyRPCsUnimplementedUntilHandlers proves the credential
// rotation policy RPCs (#1201) and the tenant delegation policy RPCs (#1187)
// pass authentication and authorization and are then answered UNIMPLEMENTED
// by the embedded UnimplementedQRDBServiceServer, not refused as
// PermissionDenied. The handlers land in #1201 part 2 and #1187 part 2,
// whose handler tests replace this test.
func TestQRDBService_PolicyRPCsUnimplementedUntilHandlers(t *testing.T) {
	mw := authmw.New(allowAllAuthorizer{},
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(admittingAuthenticator{}))
	client, cleanup := startTestServerFull(t, WithAuthMiddleware(mw))
	defer cleanup()

	ctx := context.Background()
	calls := []struct {
		name string
		call func() error
	}{
		{"CredentialSetRotationPolicy", func() error {
			_, err := client.CredentialSetRotationPolicy(ctx, &pb.CredentialSetRotationPolicyRequest{})
			return err
		}},
		{"CredentialRotationPolicy", func() error {
			_, err := client.CredentialRotationPolicy(ctx, &pb.CredentialRotationPolicyRequest{})
			return err
		}},
		{"CredentialMigrationConflicts", func() error {
			_, err := client.CredentialMigrationConflicts(ctx, &pb.CredentialMigrationConflictsRequest{})
			return err
		}},
		{"CredentialResolveMigrationConflict", func() error {
			_, err := client.CredentialResolveMigrationConflict(ctx, &pb.CredentialResolveMigrationConflictRequest{})
			return err
		}},
		{"PutDelegationPolicy", func() error {
			_, err := client.PutDelegationPolicy(ctx, &pb.PutDelegationPolicyRequest{})
			return err
		}},
		{"ListDelegationPolicies", func() error {
			_, err := client.ListDelegationPolicies(ctx, &pb.ListDelegationPoliciesRequest{})
			return err
		}},
		{"DeleteDelegationPolicy", func() error {
			_, err := client.DeleteDelegationPolicy(ctx, &pb.DeleteDelegationPolicyRequest{})
			return err
		}},
	}

	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if got := status.Code(c.call()); got != codes.Unimplemented {
				t.Fatalf("%s: code = %v, want %v", c.name, got, codes.Unimplemented)
			}
		})
	}
}
