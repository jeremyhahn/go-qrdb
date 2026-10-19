package service

import (
	"errors"
	"testing"
)

// TestErrorMessages_NonEmpty renders every typed error whose message no
// behavioural test reaches, so a message that dereferences a missing field
// panics here rather than in a caller's log line.
func TestErrorMessages_NonEmpty(t *testing.T) {
	cause := errors.New("cause")
	cases := map[string]error{
		"EncodeRequestError":                       &EncodeRequestError{Err: cause},
		"SubsystemNotConfiguredError":              &SubsystemNotConfiguredError{Subsystem: "vector"},
		"MemberNotObserverError":                   &MemberNotObserverError{ShardID: 1, ReplicaID: 2},
		"UnsupportedTransactionOpError":            &UnsupportedTransactionOpError{},
		"ImportArchiveExpandedTooLargeError":       &ImportArchiveExpandedTooLargeError{Max: 1},
		"ImportTooManyEntriesError":                &ImportTooManyEntriesError{Max: 1},
		"ImportTooManyRowsError":                   &ImportTooManyRowsError{Max: 1},
		"ImportProposalTooLargeError":              &ImportProposalTooLargeError{Max: 1},
		"nodeClosedError":                          &nodeClosedError{},
		"nilRequestError":                          &nilRequestError{},
		"ceremonyNotConfiguredError":               &ceremonyNotConfiguredError{},
		"SystemBarrierInitRefusedError":            &SystemBarrierInitRefusedError{},
		"InvalidRaftTopologyError":                 &InvalidRaftTopologyError{Database: "db", Mode: "m", Reason: "r"},
		"RaftDistributedUnavailableError":          &RaftDistributedUnavailableError{Database: "db"},
		"ReplicaFloorError":                        &ReplicaFloorError{Database: "db"},
		"NotDedicatedRaftDatabaseError":            &NotDedicatedRaftDatabaseError{Database: "db"},
		"ShardLeaderTimeoutError":                  &ShardLeaderTimeoutError{ShardID: 1},
		"DatabaseCreateTimeoutError":               &DatabaseCreateTimeoutError{Name: "db", Err: cause},
		"ImportSessionNotFoundError":               &ImportSessionNotFoundError{SessionID: "s"},
		"ImportSessionOwnerError":                  &ImportSessionOwnerError{SessionID: "s"},
		"ImportSequenceError":                      &ImportSequenceError{SessionID: "s", Got: 2, Want: 1},
		"ImportQuotaExceededError":                 &ImportQuotaExceededError{Kind: "bytes", Limit: 1},
		"ImportSessionCorruptError":                &ImportSessionCorruptError{SessionID: "s", Reason: "r"},
		"ClusterJoinRequestReviewUnsupportedError": &ClusterJoinRequestReviewUnsupportedError{},
		"MemberPromotionUnsupportedError":          &MemberPromotionUnsupportedError{},
		"ReplicatedAuditNotEntitledError":          &ReplicatedAuditNotEntitledError{},
		"AdminAlreadyBoundError":                   &AdminAlreadyBoundError{},
		"AdminRebindNoBinderError":                 &AdminRebindNoBinderError{},
		"AdminRebindLookupError":                   &AdminRebindLookupError{Cause: cause},
		"AdminRebindAdminNotFoundError":            &AdminRebindAdminNotFoundError{},
		"AdminRebindBindError":                     &AdminRebindBindError{Cause: cause},
		"SignCSRThrottledError":                    &SignCSRThrottledError{},
		"CollectionNotActiveError":                 &CollectionNotActiveError{},
		"CollectionStateConflictError":             &CollectionStateConflictError{},
		"CollectionDocumentsChangingError":         &CollectionDocumentsChangingError{},
		"CollectionDocumentConflictError":          &CollectionDocumentConflictError{},
		"HardConfinedImportGuardUnavailableError":  &HardConfinedImportGuardUnavailableError{},
		"MarkerContendedError":                     &MarkerContendedError{Key: "k"},
		"TenantNotFoundError":                      &TenantNotFoundError{},
		"DatabaseNotFoundError":                    &DatabaseNotFoundError{},
		"BarrierSealedError":                       &BarrierSealedError{},
		"IsolationBackendUnavailableError":         &IsolationBackendUnavailableError{},
		"SQLEmptyQueryError":                       &SQLEmptyQueryError{},
		"SQLEmptyDatabaseError":                    &SQLEmptyDatabaseError{},
		"SQLExecutionError":                        &SQLExecutionError{Query: "SELECT 1", Err: cause},
		"TableForeignKeyAlreadyExistsError":        &TableForeignKeyAlreadyExistsError{},
		"TableForeignKeyNotFoundError":             &TableForeignKeyNotFoundError{},
		"TableForeignKeyNoColumnsError":            &TableForeignKeyNoColumnsError{},
		"TableForeignKeyColumnNotFoundError":       &TableForeignKeyColumnNotFoundError{},
		"TableGenerationStaleError":                &TableGenerationStaleError{Database: "db", Name: "t", Expected: 1, Actual: 2},
		"ViewAlreadyExistsError":                   &ViewAlreadyExistsError{},
		"ViewNotFoundError":                        &ViewNotFoundError{},
		"ViewDefinitionEmptyError":                 &ViewDefinitionEmptyError{},
		"TableCheckAlreadyExistsError":             &TableCheckAlreadyExistsError{},
		"TableCheckNotFoundError":                  &TableCheckNotFoundError{},
		"notStoredError":                           &notStoredError{err: cause},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if err.Error() == "" {
				t.Fatalf("%s: Error() is empty", name)
			}
		})
	}
}

// TestErrorMessages_UnwrapCause pins that each wrapping error hands its
// cause to errors.Is.
func TestErrorMessages_UnwrapCause(t *testing.T) {
	cause := errors.New("cause")
	cases := map[string]error{
		"AdminRebindLookupError": &AdminRebindLookupError{Cause: cause},
		"AdminRebindBindError":   &AdminRebindBindError{Cause: cause},
		"SQLExecutionError":      &SQLExecutionError{Err: cause},
		"notStoredError":         &notStoredError{err: cause},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, cause) {
				t.Fatalf("%s: errors.Is(err, cause) = false", name)
			}
		})
	}
}
