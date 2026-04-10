// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vtab

import "strings"

// InvalidPragmaValueError is returned when a PRAGMA statement is given an
// unrecognized value. Pragma is the name of the PRAGMA that was set; Value is
// the unrecognized string that was supplied; Valid lists the accepted values.
type InvalidPragmaValueError struct {
	Pragma string
	Value  string
	Valid  []string
}

// Error returns a human-readable description that includes the offending value
// and the full set of accepted values.
func (e *InvalidPragmaValueError) Error() string {
	return "vtab: invalid value " + `"` + e.Value + `"` +
		" for PRAGMA " + e.Pragma +
		": valid values are " + strings.Join(e.Valid, ", ")
}

// KVGetError is returned when a KV get operation fails.
// Key is the key that was requested; Err is the underlying failure.
type KVGetError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the get failure.
func (e *KVGetError) Error() string {
	return "vtab: kv get failed for key " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KVGetError) Unwrap() error {
	return e.Err
}

// KVPutError is returned when a KV put operation fails.
// Key is the key that was written; Err is the underlying failure.
type KVPutError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the put failure.
func (e *KVPutError) Error() string {
	return "vtab: kv put failed for key " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KVPutError) Unwrap() error {
	return e.Err
}

// KVDeleteError is returned when a KV delete operation fails.
// Key is the key that was deleted; Err is the underlying failure.
type KVDeleteError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the delete failure.
func (e *KVDeleteError) Error() string {
	return "vtab: kv delete failed for key " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KVDeleteError) Unwrap() error {
	return e.Err
}

// KVScanError is returned when a KV scan operation fails.
// Prefix is the key prefix that was scanned; Err is the underlying failure.
type KVScanError struct {
	Prefix string
	Err    error
}

// Error returns a human-readable description of the scan failure.
func (e *KVScanError) Error() string {
	return "vtab: kv scan failed for prefix " + `"` + e.Prefix + `"` + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KVScanError) Unwrap() error {
	return e.Err
}

// ModuleCreateError is returned when a virtual table module cannot be
// registered with the SQLite connection. Module is the module name;
// Err is the underlying failure.
type ModuleCreateError struct {
	Module string
	Err    error
}

// Error returns a human-readable description of the module registration failure.
func (e *ModuleCreateError) Error() string {
	return "vtab: module create failed for " + e.Module + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ModuleCreateError) Unwrap() error {
	return e.Err
}

// CollectionGetError is returned when a collection document get operation fails.
// Database and Collection identify the target; ID is the requested document ID;
// Err is the underlying failure.
type CollectionGetError struct {
	Database   string
	Collection string
	ID         uint64
	Err        error
}

// Error returns a human-readable description of the get failure.
func (e *CollectionGetError) Error() string {
	return "vtab: collection get failed for " + e.Database + "/" + e.Collection +
		" id " + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CollectionGetError) Unwrap() error {
	return e.Err
}

// CollectionInsertError is returned when a collection document insert fails.
// Database and Collection identify the target; Err is the underlying failure.
type CollectionInsertError struct {
	Database   string
	Collection string
	Err        error
}

// Error returns a human-readable description of the insert failure.
func (e *CollectionInsertError) Error() string {
	return "vtab: collection insert failed for " + e.Database + "/" + e.Collection + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CollectionInsertError) Unwrap() error {
	return e.Err
}

// CollectionUpdateError is returned when a collection document update fails.
// Database and Collection identify the target; ID is the document being updated;
// Err is the underlying failure.
type CollectionUpdateError struct {
	Database   string
	Collection string
	ID         uint64
	Err        error
}

// Error returns a human-readable description of the update failure.
func (e *CollectionUpdateError) Error() string {
	return "vtab: collection update failed for " + e.Database + "/" + e.Collection +
		" id " + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CollectionUpdateError) Unwrap() error {
	return e.Err
}

// CollectionDeleteError is returned when a collection document delete fails.
// Database and Collection identify the target; ID is the document being deleted;
// Err is the underlying failure.
type CollectionDeleteError struct {
	Database   string
	Collection string
	ID         uint64
	Err        error
}

// Error returns a human-readable description of the delete failure.
func (e *CollectionDeleteError) Error() string {
	return "vtab: collection delete failed for " + e.Database + "/" + e.Collection +
		" id " + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CollectionDeleteError) Unwrap() error {
	return e.Err
}

// CollectionListError is returned when a collection document list operation fails.
// Database and Collection identify the target; Err is the underlying failure.
type CollectionListError struct {
	Database   string
	Collection string
	Err        error
}

// Error returns a human-readable description of the list failure.
func (e *CollectionListError) Error() string {
	return "vtab: collection list failed for " + e.Database + "/" + e.Collection + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CollectionListError) Unwrap() error {
	return e.Err
}

// DiscoveryError is returned when the table auto-discovery process fails,
// either because the metadata provider could not be contacted or because a
// generated CREATE VIRTUAL TABLE statement could not be executed.
// Cause holds the underlying error.
type DiscoveryError struct {
	Cause error
}

// Error returns a human-readable description of the discovery failure.
func (e *DiscoveryError) Error() string {
	return "vtab: table discovery failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause so that callers can use errors.Is and
// errors.As to inspect the root failure.
func (e *DiscoveryError) Unwrap() error {
	return e.Cause
}

// EntityGetError is returned when an entity get operation fails.
// EntityType is the entity type name; ID is the entity ID; Err is the cause.
type EntityGetError struct {
	EntityType string
	ID         uint64
	Err        error
}

// Error returns a human-readable description of the get failure.
func (e *EntityGetError) Error() string {
	return "vtab: entity get failed for " + e.EntityType + "/" + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EntityGetError) Unwrap() error { return e.Err }

// EntityPutError is returned when an entity put operation fails.
// EntityType is the entity type name; ID is the entity ID; Err is the cause.
type EntityPutError struct {
	EntityType string
	ID         uint64
	Err        error
}

// Error returns a human-readable description of the put failure.
func (e *EntityPutError) Error() string {
	return "vtab: entity put failed for " + e.EntityType + "/" + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EntityPutError) Unwrap() error { return e.Err }

// EntityDeleteError is returned when an entity delete operation fails.
// EntityType is the entity type name; ID is the entity ID; Err is the cause.
type EntityDeleteError struct {
	EntityType string
	ID         uint64
	Err        error
}

// Error returns a human-readable description of the delete failure.
func (e *EntityDeleteError) Error() string {
	return "vtab: entity delete failed for " + e.EntityType + "/" + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EntityDeleteError) Unwrap() error { return e.Err }

// EntityScanError is returned when an entity scan operation fails.
// EntityType is the entity type name; Err is the cause.
type EntityScanError struct {
	EntityType string
	Err        error
}

// Error returns a human-readable description of the scan failure.
func (e *EntityScanError) Error() string {
	return "vtab: entity scan failed for " + e.EntityType + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EntityScanError) Unwrap() error { return e.Err }

// EntityIndexError is returned when an entity index query fails.
// EntityType is the entity type name; FieldName is the indexed field; Err is
// the cause.
type EntityIndexError struct {
	EntityType string
	FieldName  string
	Err        error
}

// Error returns a human-readable description of the index query failure.
func (e *EntityIndexError) Error() string {
	return "vtab: entity index query failed for " + e.EntityType + "." + e.FieldName + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EntityIndexError) Unwrap() error { return e.Err }

// NamespaceCreateError is returned when a vector namespace create operation fails.
// Name is the namespace name; Err is the underlying failure.
type NamespaceCreateError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the create failure.
func (e *NamespaceCreateError) Error() string {
	return "vtab: namespace create failed for " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *NamespaceCreateError) Unwrap() error { return e.Err }

// NamespaceDeleteError is returned when a vector namespace delete operation fails.
// Name is the namespace name; Err is the underlying failure.
type NamespaceDeleteError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the delete failure.
func (e *NamespaceDeleteError) Error() string {
	return "vtab: namespace delete failed for " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *NamespaceDeleteError) Unwrap() error { return e.Err }

// NamespaceListError is returned when a vector namespace list operation fails.
// Err is the underlying failure.
type NamespaceListError struct {
	Err error
}

// Error returns a human-readable description of the list failure.
func (e *NamespaceListError) Error() string {
	return "vtab: namespace list failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *NamespaceListError) Unwrap() error { return e.Err }

// NamespaceGetError is returned when a vector namespace get operation fails.
// Name is the namespace name; Err is the underlying failure.
type NamespaceGetError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the get failure.
func (e *NamespaceGetError) Error() string {
	return "vtab: namespace get failed for " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *NamespaceGetError) Unwrap() error { return e.Err }

// VectorUpsertError is returned when a vector upsert operation fails.
// Namespace is the target namespace; ID is the vector ID; Err is the cause.
type VectorUpsertError struct {
	Namespace string
	ID        uint64
	Err       error
}

// Error returns a human-readable description of the upsert failure.
func (e *VectorUpsertError) Error() string {
	return "vtab: vector upsert failed for " + e.Namespace + "/" + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorUpsertError) Unwrap() error { return e.Err }

// VectorGetError is returned when a vector get operation fails.
// Namespace is the target namespace; ID is the vector ID; Err is the cause.
type VectorGetError struct {
	Namespace string
	ID        uint64
	Err       error
}

// Error returns a human-readable description of the get failure.
func (e *VectorGetError) Error() string {
	return "vtab: vector get failed for " + e.Namespace + "/" + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorGetError) Unwrap() error { return e.Err }

// VectorDeleteError is returned when a vector delete operation fails.
// Namespace is the target namespace; ID is the vector ID; Err is the cause.
type VectorDeleteError struct {
	Namespace string
	ID        uint64
	Err       error
}

// Error returns a human-readable description of the delete failure.
func (e *VectorDeleteError) Error() string {
	return "vtab: vector delete failed for " + e.Namespace + "/" + uint64ToStr(e.ID) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorDeleteError) Unwrap() error { return e.Err }

// VectorListError is returned when a vector list operation fails.
// Namespace is the target namespace; Err is the underlying failure.
type VectorListError struct {
	Namespace string
	Err       error
}

// Error returns a human-readable description of the list failure.
func (e *VectorListError) Error() string {
	return "vtab: vector list failed for " + e.Namespace + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorListError) Unwrap() error { return e.Err }

// DatapointWriteError is returned when a time-series datapoint write fails.
// Metric identifies the target metric; Err is the underlying failure.
type DatapointWriteError struct {
	Metric string
	Err    error
}

// Error returns a human-readable description of the write failure.
func (e *DatapointWriteError) Error() string {
	if e.Metric == "" {
		return "vtab: datapoint write failed: " + e.Err.Error()
	}
	return "vtab: datapoint write failed for metric " + `"` + e.Metric + `"` + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DatapointWriteError) Unwrap() error { return e.Err }

// DatapointQueryError is returned when a time-series datapoint query fails.
// Metric identifies the target metric; Err is the underlying failure.
type DatapointQueryError struct {
	Metric string
	Err    error
}

// Error returns a human-readable description of the query failure.
func (e *DatapointQueryError) Error() string {
	return "vtab: datapoint query failed for metric " + `"` + e.Metric + `"` + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DatapointQueryError) Unwrap() error { return e.Err }

// DatapointScanError is returned when a full scan of all datapoints fails.
// Err is the underlying failure.
type DatapointScanError struct {
	Err error
}

// Error returns a human-readable description of the scan failure.
func (e *DatapointScanError) Error() string {
	return "vtab: datapoint scan failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DatapointScanError) Unwrap() error { return e.Err }

// MetricsListError is returned when a time-series metrics list operation fails,
// or when a mutation is attempted on the read-only metrics virtual table.
// Err is the underlying failure.
type MetricsListError struct {
	Err error
}

// Error returns a human-readable description of the list failure.
func (e *MetricsListError) Error() string {
	return "vtab: metrics list failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetricsListError) Unwrap() error { return e.Err }

// MetricsGetError is returned when fetching a single metric by name fails.
// Name identifies the metric that was requested; Err is the underlying failure.
type MetricsGetError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the get failure.
func (e *MetricsGetError) Error() string {
	return "vtab: metrics get failed for " + `"` + e.Name + `"` + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetricsGetError) Unwrap() error { return e.Err }

// uint64ToStr converts a uint64 to its decimal string representation without
// importing strconv, keeping allocation overhead minimal.
func uint64ToStr(n uint64) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
