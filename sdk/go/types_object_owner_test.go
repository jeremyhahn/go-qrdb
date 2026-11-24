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
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: auth.UserSubject(1), UserID: 1})
	require.NoError(t, sdk.OwnObject(ctx, "1", "read"))

	err := sdk.OwnObject(ctx, "2", "read")
	var denied *sdk.ObjectOwnerDeniedError
	require.True(t, errors.As(err, &denied), "error %v is not *sdk.ObjectOwnerDeniedError", err)
	require.Equal(t, "2", denied.Owner)
	require.ErrorIs(t, err, auth.ErrUnauthorized)

	// A holder identity with no bound account never owns an object, even
	// when its name reads like the owner's user id.
	holder := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "1"})
	require.ErrorIs(t, sdk.OwnObject(holder, "1", "read"), auth.ErrUnauthorized)

	owned := sdk.WithResourceOwner(context.Background(), "user-1")
	owner, ok := sdk.ResourceOwnerFromContext(owned)
	require.True(t, ok)
	require.Equal(t, "user-1", owner)
}

func TestSDK_ABACOwnsResourceCondition(t *testing.T) {
	cond, err := sdk.NewABACConditionParser(nil).Parse("subject.owns_resource")
	require.NoError(t, err)

	subject := &abac.Subject{ID: "1", UserID: 1}
	require.True(t, cond.Evaluate(&abac.EvalContext{
		Subject: subject,
		Request: map[string]interface{}{sdk.ABACResourceOwnerContextKey: "1"},
	}))
	require.False(t, cond.Evaluate(&abac.EvalContext{
		Subject: subject,
		Request: map[string]interface{}{sdk.ABACResourceOwnerContextKey: "2"},
	}))
	require.False(t, cond.Evaluate(&abac.EvalContext{
		Subject: &abac.Subject{ID: "1"},
		Request: map[string]interface{}{sdk.ABACResourceOwnerContextKey: "1"},
	}), "an unbound subject never owns an object")
	require.False(t, cond.Evaluate(&abac.EvalContext{
		Subject: subject,
		Request: map[string]interface{}{},
	}))
}
