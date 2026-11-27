// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"errors"
	"strings"
	"testing"
)

func TestCatalogErrors_MessagesNameTheSubject(t *testing.T) {
	cause := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"PoolNotFoundError", &PoolNotFoundError{Pool: "fast"}, []string{"fast"}},
		{"PoolAlreadyExistsError", &PoolAlreadyExistsError{Pool: "fast"}, []string{"fast"}},
		{"PoolInUseError", &PoolInUseError{Pool: "fast", Tenants: []string{"acme", "widgets"}}, []string{"fast", "acme", "widgets"}},
		{"PoolCapacityExceededError", &PoolCapacityExceededError{Pool: "fast", Requested: 200, Available: 100}, []string{"fast", "200", "100"}},
		{"InvalidPoolRequestError", &InvalidPoolRequestError{Reason: "empty name"}, []string{"empty name"}},
		{"InvalidAssignmentPathError", &InvalidAssignmentPathError{TenantID: "acme", Path: ".."}, []string{"acme", ".."}},
		{"AssignmentNotFoundError", &AssignmentNotFoundError{TenantID: "acme"}, []string{"acme"}},
		{"TenantAlreadyAssignedError", &TenantAlreadyAssignedError{TenantID: "acme", CurrentPool: "fast", RequestedPool: "bulk"}, []string{"acme", "fast", "bulk"}},
		{"ReservationConflictError", &ReservationConflictError{Pool: "fast"}, []string{"fast"}},
		{"MoveNotImplementedError", &MoveNotImplementedError{}, []string{"not wired"}},
		{"MoveNotFoundError", &MoveNotFoundError{TenantID: "acme"}, []string{`"acme"`, "no move"}},
		{"MoveConflictError", &MoveConflictError{TenantID: "acme", NodeID: "n1", Reason: "destination pool is not ready on this node"}, []string{"acme", "n1", "not ready"}},
		{"MoveConflictError no node", &MoveConflictError{TenantID: "acme", Reason: "a move is already running"}, []string{"acme", "already running"}},
		{"MoveToUnencryptedPoolError", &MoveToUnencryptedPoolError{TenantID: "acme", FromPool: "fast", ToPool: "bulk"}, []string{"acme", "fast", "bulk", "not", "allow_unencrypted_destination", "--allow-unencrypted-destination"}},
		{"MoveEncryptionDropUnauditedError", &MoveEncryptionDropUnauditedError{TenantID: "acme", FromPool: "fast", ToPool: "bulk"}, []string{"acme", "fast", "bulk", "audit sink"}},
		{"MoveJournalConflictError", &MoveJournalConflictError{TenantID: "acme"}, []string{`"acme"`, "retry"}},
		{"InvalidMoveRequestError", &InvalidMoveRequestError{Reason: "no replica nodes"}, []string{"no replica nodes"}},
		{"InvalidCatalogDependencyError", &InvalidCatalogDependencyError{Reason: "nil KVServicer"}, []string{"nil KVServicer"}},
		{"EncodeError", &EncodeError{Err: cause}, []string{"boom"}},
		{"CorruptRecordError", &CorruptRecordError{Key: "k", Err: cause}, []string{"k", "boom"}},
		{"InvalidPageRequestError", &InvalidPageRequestError{Field: "page_size", Reason: "must not be negative"}, []string{"page_size", "must not be negative"}},
		{"PoolUnavailableError", &PoolUnavailableError{Pool: "fast", NodeID: "node-1", Err: cause}, []string{"fast", "node-1", "boom"}},
		{"PoolUnavailableError no cause", &PoolUnavailableError{Pool: "fast", NodeID: "node-1"}, []string{"fast", "node-1"}},
		{"VolumeAlreadyOpenError", &VolumeAlreadyOpenError{Pool: "fast"}, []string{"fast"}},
		{"ProviderMismatchError", &ProviderMismatchError{Pool: "fast", CatalogProvider: "luks2", ConfiguredProvider: "dir"}, []string{"fast", "luks2", "dir"}},
		{"PoolUnparseableAssignmentError", &PoolUnparseableAssignmentError{Pool: "fast"}, []string{"fast"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := c.err.Error()
			if msg == "" {
				t.Fatal("Error() returned an empty string")
			}
			for _, want := range c.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("Error() = %q, want it to contain %q", msg, want)
				}
			}
		})
	}
}

func TestCatalogErrors_UnwrapReachesCause(t *testing.T) {
	cause := errors.New("boom")
	cases := []error{
		&EncodeError{Err: cause},
		&CorruptRecordError{Key: "k", Err: cause},
		&PoolUnavailableError{Pool: "fast", NodeID: "node-1", Err: cause},
	}
	for _, err := range cases {
		if !errors.Is(err, cause) {
			t.Fatalf("errors.Is(%v, cause) = false, want true", err)
		}
	}
}
