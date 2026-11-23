package tenant

import (
	"strconv"
	"strings"
)

// Status is a tenant's operational status. A tenant is created
// StatusActive; a system administrator may suspend or disable it and
// reactivate it again. Only StatusActive reads as active: an empty or
// unknown value — including a record written before the field existed —
// fails closed.
type Status string

const (
	// StatusActive is a tenant in normal service. New tenants start here.
	StatusActive Status = "active"

	// StatusSuspended is a tenant temporarily out of service, typically
	// pending an administrative action; it is expected to be reactivated.
	StatusSuspended Status = "suspended"

	// StatusDisabled is a tenant taken out of service until a system
	// administrator reactivates it.
	StatusDisabled Status = "disabled"
)

// statusTransitions is the authoritative transition table: for each status,
// the set of statuses it may move to. A status not in the table is not a
// status.
var statusTransitions = map[Status]map[Status]struct{}{
	StatusActive: {
		StatusSuspended: {},
		StatusDisabled:  {},
	},
	StatusSuspended: {
		StatusActive:   {},
		StatusDisabled: {},
	},
	StatusDisabled: {
		StatusActive: {},
	},
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	_, ok := statusTransitions[s]
	return ok
}

// IsActive reports whether s is StatusActive. An empty or unknown status is
// never active.
func (s Status) IsActive() bool {
	return s == StatusActive
}

// CanTransitionTo reports whether a tenant in status s may move to next.
// Staying in the same known status is always allowed. A record with an empty
// or unknown status may move only to a known status, so a system
// administrator can repair it.
func (s Status) CanTransitionTo(next Status) bool {
	if !next.Valid() {
		return false
	}
	if !s.Valid() || s == next {
		return true
	}
	_, ok := statusTransitions[s][next]
	return ok
}

// String returns the wire form of s.
func (s Status) String() string { return string(s) }

// knownStatuses lists every status in a stable order, for error messages.
func knownStatuses() []Status {
	return []Status{StatusActive, StatusSuspended, StatusDisabled}
}

// applyStatus moves info to the status patch sets, if any. An absent status
// leaves info unchanged; a refused one leaves it unchanged and returns
// *InvalidStatusError or *StatusTransitionError.
func applyStatus(info *TenantInfo, patch TenantPatch) error {
	if patch.Status == nil {
		return nil
	}
	to := *patch.Status
	if !to.Valid() {
		return &InvalidStatusError{Value: to, Allowed: knownStatuses()}
	}
	if !info.Status.CanTransitionTo(to) {
		return &StatusTransitionError{TenantID: info.ID, From: info.Status, To: to}
	}
	info.Status = to
	return nil
}

// FilterByStatus narrows a tenant listing to the tenants in one status.
// Every API surface applies it after the caller's visibility narrowing
// (authmw.FilterTenantList), so the transports cannot drift; the servicer's
// ListTenants stays unfiltered for the internal callers that need every
// tenant. An empty status is no filter and returns infos unchanged. A known
// status keeps only the tenants whose stored Status equals it: a legacy
// record with an empty status matches no filter (fail-closed, as IsActive).
// An unknown value, including go-xkms's "deleted" (deleted tenants are
// retired ids in the deleted-tenant tombstone, listed by
// ListDeletedTenants), is refused with *InvalidStatusError.
func FilterByStatus(infos []TenantInfo, status Status) ([]TenantInfo, error) {
	if status == "" {
		return infos, nil
	}
	if !status.Valid() {
		return nil, &InvalidStatusError{Value: status, Allowed: knownStatuses()}
	}
	out := make([]TenantInfo, 0, len(infos))
	for i := range infos {
		if infos[i].Status == status {
			out = append(out, infos[i])
		}
	}
	return out, nil
}

// InvalidStatusError is returned when a request names a status that is not
// one of the known statuses.
type InvalidStatusError struct {
	// Value is the status the caller asked for.
	Value Status

	// Allowed lists the known statuses.
	Allowed []Status
}

// Error implements error.
func (e *InvalidStatusError) Error() string {
	msg := "tenant: invalid status " + strconv.Quote(string(e.Value))
	if len(e.Allowed) > 0 {
		msg += "; allowed: " + joinStatuses(e.Allowed)
	}
	return msg
}

// StatusTransitionError is returned when a tenant is asked to move between
// two statuses the transition table does not connect.
type StatusTransitionError struct {
	// TenantID is the tenant whose transition was refused.
	TenantID string

	// From is the status the record currently holds.
	From Status

	// To is the status the caller asked for.
	To Status
}

// Error implements error.
func (e *StatusTransitionError) Error() string {
	return "tenant: cannot transition tenant " + e.TenantID +
		" from " + strconv.Quote(string(e.From)) + " to " + strconv.Quote(string(e.To))
}

// joinStatuses renders statuses as a comma-separated list.
func joinStatuses(statuses []Status) string {
	parts := make([]string, len(statuses))
	for i, s := range statuses {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}
