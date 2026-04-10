// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kvstore

// SealedError is returned when the KV store is sealed and cannot
// process requests.
type SealedError struct{}

// Error returns a human-readable description indicating the store is sealed.
func (e *SealedError) Error() string {
	return "kv store is sealed"
}

// PutError wraps a put operation error with the target key.
type PutError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the put failure.
func (e *PutError) Error() string {
	return "kvstore put " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *PutError) Unwrap() error {
	return e.Err
}

// GetError wraps a get operation error with the target key.
type GetError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the get failure.
func (e *GetError) Error() string {
	return "kvstore get " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *GetError) Unwrap() error {
	return e.Err
}

// DeleteError wraps a delete operation error with the target key.
type DeleteError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the delete failure.
func (e *DeleteError) Error() string {
	return "kvstore delete " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *DeleteError) Unwrap() error {
	return e.Err
}

// ScanError wraps a scan operation error with the prefix.
type ScanError struct {
	Prefix string
	Err    error
}

// Error returns a human-readable description of the scan failure.
func (e *ScanError) Error() string {
	return "kvstore scan " + e.Prefix + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ScanError) Unwrap() error {
	return e.Err
}

// ListError wraps a list operation error with the prefix.
type ListError struct {
	Prefix string
	Err    error
}

// Error returns a human-readable description of the list failure.
func (e *ListError) Error() string {
	return "kvstore list " + e.Prefix + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ListError) Unwrap() error {
	return e.Err
}

// ExistsError wraps an exists operation error with the target key.
type ExistsError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the exists failure.
func (e *ExistsError) Error() string {
	return "kvstore exists " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ExistsError) Unwrap() error {
	return e.Err
}

// IndexQueryError wraps an index query operation error.
type IndexQueryError struct {
	IndexName string
	Reason    string
}

// Error returns a human-readable description of the index query failure.
func (e *IndexQueryError) Error() string {
	return "kvstore index query " + e.IndexName + ": " + e.Reason
}

// IndexScanError wraps an index scan operation error.
type IndexScanError struct {
	IndexName string
	Reason    string
}

// Error returns a human-readable description of the index scan failure.
func (e *IndexScanError) Error() string {
	return "kvstore index scan " + e.IndexName + ": " + e.Reason
}

// IndexRegistrationError wraps an index registration error.
type IndexRegistrationError struct {
	EntityType string
	Err        error
}

// Error returns a human-readable description of the registration failure.
func (e *IndexRegistrationError) Error() string {
	return "kvstore index registration " + e.EntityType + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *IndexRegistrationError) Unwrap() error {
	return e.Err
}
