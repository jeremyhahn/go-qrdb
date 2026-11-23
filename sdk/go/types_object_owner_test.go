package sdk_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func TestSDK_OwnObjectAliasesAuth(t *testing.T) {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "user-1"})
	require.NoError(t, sdk.OwnObject(ctx, "user-1", "read"))

	err := sdk.OwnObject(ctx, "user-2", "read")
	var denied *sdk.ObjectOwnerDeniedError
	require.True(t, errors.As(err, &denied), "error %v is not *sdk.ObjectOwnerDeniedError", err)
	require.Equal(t, "user-2", denied.Owner)
	require.ErrorIs(t, err, auth.ErrUnauthorized)

	owned := sdk.WithResourceOwner(context.Background(), "user-1")
	owner, ok := sdk.ResourceOwnerFromContext(owned)
	require.True(t, ok)
	require.Equal(t, "user-1", owner)
}

func TestSDK_ABACOwnsResourceCondition(t *testing.T) {
	cond, err := sdk.NewABACConditionParser(nil).Parse("subject.owns_resource")
	require.NoError(t, err)

	subject := &abac.Subject{ID: "user-1"}
	require.True(t, cond.Evaluate(&abac.EvalContext{
		Subject: subject,
		Request: map[string]interface{}{sdk.ABACResourceOwnerContextKey: "user-1"},
	}))
	require.False(t, cond.Evaluate(&abac.EvalContext{
		Subject: subject,
		Request: map[string]interface{}{sdk.ABACResourceOwnerContextKey: "user-2"},
	}))
	require.False(t, cond.Evaluate(&abac.EvalContext{
		Subject: subject,
		Request: map[string]interface{}{},
	}))
}
