package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// lookupUserRecord is the subset of marshalUserRecord's output the lookup tests
// assert on.
type lookupUserRecord struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	TenantID string `json:"tenantID"`
}

func decodeUserRecord(t *testing.T, text string) lookupUserRecord {
	t.Helper()
	var rec lookupUserRecord
	if err := json.Unmarshal([]byte(text), &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rec
}

// TestHandleUserGetByUsername looks a user up by username within the
// requested tenant, refuses a call without a username, and wraps a service
// failure in a ToolError naming the tool.
func TestHandleUserGetByUsername(t *testing.T) {
	var gotTenant, gotUsername string
	svc := &mockUserService{
		getUserByUsernameFn: func(_ context.Context, tenantID, username string) (*user.User, error) {
			gotTenant, gotUsername = tenantID, username
			if username == "nobody" {
				return nil, &user.UserNotFoundError{}
			}
			return &user.User{ID: 7, Username: username, TenantID: tenantID, Lifecycle: user.LifecycleActive}, nil
		},
	}
	handler := handleUserGetByUsername(svc, nil)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_username", map[string]any{
		"tenant_id": "acme", "username": "alice",
	}))
	if err != nil || result.IsError {
		t.Fatalf("get by username: result=%+v err=%v", result, err)
	}
	if gotTenant != "acme" || gotUsername != "alice" {
		t.Fatalf("service saw (%q, %q)", gotTenant, gotUsername)
	}
	if rec := decodeUserRecord(t, resultText(t, result)); rec.Username != "alice" || rec.TenantID != "acme" {
		t.Fatalf("record = %+v", rec)
	}

	gotUsername = ""
	result, err = handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_username", map[string]any{"tenant_id": "acme"}))
	if err != nil || !result.IsError {
		t.Fatalf("missing username: result=%+v err=%v, want an argument refusal", result, err)
	}
	if gotUsername != "" {
		t.Fatalf("service called with %q on a refused request", gotUsername)
	}

	_, err = handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_username", map[string]any{"username": "nobody"}))
	var te *ToolError
	var nf *user.UserNotFoundError
	if !errors.As(err, &te) || te.Tool != "qrdb_user_get_by_username" || !errors.As(err, &nf) {
		t.Fatalf("err = %v, want ToolError(qrdb_user_get_by_username) wrapping UserNotFoundError", err)
	}
}

// TestHandleUserGetByEmail looks a user up by email within the requested
// tenant and renders the same record every other user tool returns.
func TestHandleUserGetByEmail(t *testing.T) {
	var gotTenant, gotEmail string
	svc := &mockUserService{
		getUserByEmailFn: func(_ context.Context, tenantID, email string) (*user.User, error) {
			gotTenant, gotEmail = tenantID, email
			if email == "nobody@example.com" {
				return nil, &user.UserNotFoundError{}
			}
			return &user.User{ID: 7, Username: "alice", Email: email, TenantID: tenantID, Lifecycle: user.LifecycleActive}, nil
		},
	}
	handler := handleUserGetByEmail(svc, nil)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_email", map[string]any{
		"tenant_id": "acme", "email": "alice@example.com",
	}))
	if err != nil || result.IsError {
		t.Fatalf("get by email: result=%+v err=%v", result, err)
	}
	if gotTenant != "acme" || gotEmail != "alice@example.com" {
		t.Fatalf("service saw (%q, %q)", gotTenant, gotEmail)
	}
	if rec := decodeUserRecord(t, resultText(t, result)); rec.Username != "alice" || rec.Email != "alice@example.com" || rec.TenantID != "acme" {
		t.Fatalf("record = %+v", rec)
	}

	result, err = handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_email", map[string]any{"tenant_id": "acme"}))
	if err != nil || !result.IsError {
		t.Fatalf("missing email: result=%+v err=%v, want an argument refusal", result, err)
	}

	_, err = handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_email", map[string]any{"email": "nobody@example.com"}))
	var te *ToolError
	var nf *user.UserNotFoundError
	if !errors.As(err, &te) || te.Tool != "qrdb_user_get_by_email" || !errors.As(err, &nf) {
		t.Fatalf("err = %v, want ToolError(qrdb_user_get_by_email) wrapping UserNotFoundError", err)
	}
}

// TestHandleUserGetByCertThumbprint looks a user up by certificate
// thumbprint, refuses a call without one, and wraps a service failure in a
// ToolError naming the tool.
func TestHandleUserGetByCertThumbprint(t *testing.T) {
	var gotThumbprint string
	svc := &mockUserService{
		getUserByCertThumbprintFn: func(_ context.Context, thumbprint string) (*user.User, error) {
			gotThumbprint = thumbprint
			if thumbprint == "sha256:unknown" {
				return nil, &user.UserNotFoundError{}
			}
			return &user.User{ID: 1, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, nil
		},
	}
	handler := handleUserGetByCertThumbprint(svc, nil)

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_cert_thumbprint", map[string]any{"thumbprint": "sha256:abc"}))
	if err != nil || result.IsError {
		t.Fatalf("get by thumbprint: result=%+v err=%v", result, err)
	}
	if gotThumbprint != "sha256:abc" {
		t.Fatalf("service saw thumbprint %q", gotThumbprint)
	}
	if rec := decodeUserRecord(t, resultText(t, result)); rec.Username != "alice" || rec.TenantID != "acme" {
		t.Fatalf("record = %+v", rec)
	}

	gotThumbprint = ""
	result, err = handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_cert_thumbprint", map[string]any{}))
	if err != nil || !result.IsError {
		t.Fatalf("missing thumbprint: result=%+v err=%v, want an argument refusal", result, err)
	}
	if gotThumbprint != "" {
		t.Fatalf("service called with %q on a refused request", gotThumbprint)
	}

	_, err = handler(context.Background(), buildCallToolRequest(t, "qrdb_user_get_by_cert_thumbprint", map[string]any{"thumbprint": "sha256:unknown"}))
	var te *ToolError
	var nf *user.UserNotFoundError
	if !errors.As(err, &te) || te.Tool != "qrdb_user_get_by_cert_thumbprint" || !errors.As(err, &nf) {
		t.Fatalf("err = %v, want ToolError(qrdb_user_get_by_cert_thumbprint) wrapping UserNotFoundError", err)
	}
}
