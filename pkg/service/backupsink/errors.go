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

package backupsink

// EncodeError indicates a backup could not be serialized for persistence.
type EncodeError struct {
	ID  string
	Err error
}

func (e *EncodeError) Error() string {
	return "backupsink: encode backup " + e.ID + ": " + e.Err.Error()
}

func (e *EncodeError) Unwrap() error { return e.Err }

// DecodeError indicates a persisted backup could not be deserialized.
type DecodeError struct {
	ID  string
	Err error
}

func (e *DecodeError) Error() string {
	return "backupsink: decode backup " + e.ID + ": " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error { return e.Err }
