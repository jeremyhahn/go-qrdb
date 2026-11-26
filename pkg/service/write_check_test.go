package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWriteCheck_RunsChecksInOrderAndStopsAtTheFirstRefusal covers the
// context-carried write checks.
func TestWriteCheck_RunsChecksInOrderAndStopsAtTheFirstRefusal(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, CheckWrite(ctx), "no checks admit the write")
	require.Equal(t, ctx, WithWriteCheck(ctx, nil), "a nil check leaves the context alone")

	var ran []string
	ctx = WithWriteCheck(ctx, func(context.Context) error { ran = append(ran, "first"); return nil })
	ctx = WithWriteCheck(ctx, func(context.Context) error { ran = append(ran, "second"); return errHomeFault })
	ctx = WithWriteCheck(ctx, func(context.Context) error { ran = append(ran, "third"); return nil })
	require.ErrorIs(t, CheckWrite(ctx), errHomeFault)
	require.Equal(t, []string{"first", "second"}, ran)
}

// TestWriteGuard_EqualComparesEveryFrame covers WriteGuard.Equal.
func TestWriteGuard_EqualComparesEveryFrame(t *testing.T) {
	a := WriteGuard{compares: [][]byte{[]byte("x"), []byte("y")}}
	require.True(t, a.Equal(WriteGuard{compares: [][]byte{[]byte("x"), []byte("y")}}))
	require.False(t, a.Equal(WriteGuard{compares: [][]byte{[]byte("x")}}))
	require.False(t, a.Equal(WriteGuard{compares: [][]byte{[]byte("x"), []byte("z")}}))
	require.True(t, WriteGuard{}.Equal(WriteGuard{}))
}

// TestServiceErrors_MessagesNameTheirSubject covers the error messages of
// the tenant home, strict-isolation and log refusals.
func TestServiceErrors_MessagesNameTheirSubject(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"audit home":          {&AuditHomeNotActiveError{TenantID: "acme"}, `"acme"`},
		"key not found":       {&KeyNotFoundError{Key: "k1"}, "k1"},
		"txn result":          {&TransactionResultError{Err: errHomeFault}, errHomeFault.Error()},
		"strict shared store": {&StrictTenantSharedStoreError{TenantID: "acme", Database: "orders"}, "orders"},
		"factor set sealed":   {&FactorSetSealedError{UserID: 42}, "42"},
		"vector home":         {&TenantVectorHomeError{TenantID: "acme"}, "no tenant home resolver"},
		"vector home cause":   {&TenantVectorHomeError{TenantID: "acme", Err: errHomeFault}, errHomeFault.Error()},
		"database logs exist": {&DatabaseLogsExistError{TenantID: "acme", Database: "orders", Logs: 3}, "3 verifiable"},
		"reserved key":        {&ReservedKeyError{Key: "k"}, "reserved verifiable-log keyspace"},
		"reserved key range":  {&ReservedKeyError{Key: "k", Range: true}, "delete range"},
	}
	for name, tc := range cases {
		require.Contains(t, tc.err.Error(), tc.want, name)
	}
	require.Equal(t, "k1", (&KeyNotFoundError{Key: "k1"}).KeyNotFound())
}
