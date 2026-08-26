// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// dek_accesses counts DEK READS, not attempts to read.
//
// go-qrdb#149: between #82 and this change the counter was incremented before
// the borrow was attempted, so a refusal against an uninitialized or destroyed
// buffer — where the callback never ran and no key was touched — counted as an
// access. Nothing covered the counter in either direction, which is how the
// drift went unnoticed: it is published as "dek_accesses" through GetMetrics
// and a dashboard would have read it as reads.
//
// Both directions are asserted here, and the refusal case is the load-bearing
// one. A happy-path-only test passes just as happily when every refusal is
// counted too.
func TestClusterDEKManager_DEKAccesses_CountsReadsNotAttempts(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewClusterDEKManager(newMockEncryptionBarrier(), discardLogger())
	require.NoError(t, err)
	defer func() { _ = mgr.Close() }()
	require.NoError(t, mgr.Initialize(ctx, AlgorithmAES256GCM, 32))

	before := mgr.GetMetrics()["dek_accesses"]

	// A successful borrow counts exactly once.
	require.NoError(t, mgr.withCurrentDEK("test", func(dek []byte) error {
		require.Len(t, dek, 32)
		return nil
	}))
	require.Equal(t, before+1, mgr.GetMetrics()["dek_accesses"],
		"a successful borrow must count exactly one access")

	// A borrow whose callback fails still READ the key, so it still counts:
	// the counter is about the key having been exposed, not about the caller's
	// outcome.
	afterOK := mgr.GetMetrics()["dek_accesses"]
	require.Error(t, mgr.withCurrentDEK("test", func([]byte) error {
		return errSentinelForMetrics
	}))
	require.Equal(t, afterOK+1, mgr.GetMetrics()["dek_accesses"],
		"a borrow that ran and then failed still read the DEK")

	// A refusal must NOT count: the callback never runs and no key is touched.
	afterErr := mgr.GetMetrics()["dek_accesses"]
	livePlaintext(mgr).Destroy()

	var notInit *NotInitializedError
	err = mgr.withCurrentDEK("test", func([]byte) error {
		t.Error("the callback must not run against a destroyed buffer")
		return nil
	})
	require.ErrorAs(t, err, &notInit)
	require.Equal(t, afterErr, mgr.GetMetrics()["dek_accesses"],
		"go-qrdb#149: a refused borrow counted as a DEK access")
}

// errSentinelForMetrics is returned by a callback that must be seen to have
// run. It exists only so the test above can distinguish "the borrow happened
// and fn failed" from "the borrow was refused".
var errSentinelForMetrics = &CryptoError{Op: "test", Detail: "callback failed"}
