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

package main

import (
	"sort"
	"strconv"
	"strings"
)

// Typed errors, per the repository convention: every failure this tool can have
// is a named type a caller can assert on, never a formatted string.

// ConfigError reports a flag or environment value this tool cannot run without.
type ConfigError struct {
	// What names the setting, e.g. "-repo".
	What string
	// Why explains what is wrong with it.
	Why string
}

func (e *ConfigError) Error() string {
	return "nightlysignal: " + e.What + ": " + e.Why
}

// JobCoverageError reports that the -job list this tool was handed does not
// match the jobs the workflow file declares.
//
// THIS IS THE DISCOVERY FLOOR, and it is the whole reason a signal job can be
// trusted. Every decision below is taken per -job; a job nobody passed is a job
// that can fail forever with nothing watching, and the tool would report
// success having looked at it never. Adding a job to nightly.yml therefore has
// to add it here too, and forgetting turns the next run red instead of quiet.
type JobCoverageError struct {
	// WorkflowFile is the file whose `jobs:` block was read.
	WorkflowFile string
	// Missing are jobs the workflow declares that no -job flag covers.
	Missing []string
	// Unexpected are -job keys the workflow does not declare.
	Unexpected []string
}

func (e *JobCoverageError) Error() string {
	var b strings.Builder
	b.WriteString("nightlysignal: the -job list does not match the jobs in ")
	b.WriteString(e.WorkflowFile)
	if len(e.Missing) > 0 {
		b.WriteString("; declared but not passed: ")
		b.WriteString(strings.Join(e.Missing, ", "))
	}
	if len(e.Unexpected) > 0 {
		b.WriteString("; passed but not declared: ")
		b.WriteString(strings.Join(e.Unexpected, ", "))
	}
	return b.String()
}

// JobSpecError reports a -job flag that does not parse, or that names a display
// name disagreeing with the workflow file.
type JobSpecError struct {
	// Spec is the raw flag value.
	Spec string
	// Why explains the rejection.
	Why string
}

func (e *JobSpecError) Error() string {
	return "nightlysignal: bad -job " + strconv.Quote(e.Spec) + ": " + e.Why
}

// UnknownResultError reports a job result that is not one of the four values
// Actions can produce.
//
// Refused rather than mapped to "not red". An unrecognised result is a result
// this tool cannot reason about, and treating it as green would close a
// tracking issue on a run that proved nothing.
type UnknownResultError struct {
	// Job is the workflow job key.
	Job string
	// Result is the value that was not recognised.
	Result string
}

func (e *UnknownResultError) Error() string {
	return "nightlysignal: job " + e.Job + " reported result " +
		strconv.Quote(e.Result) + ", which is not one of success, failure, cancelled, skipped"
}

// WorkflowParseError reports a workflow file that could not be read or parsed.
type WorkflowParseError struct {
	// Path is the workflow file.
	Path string
	// Err is the underlying failure.
	Err error
}

func (e *WorkflowParseError) Error() string {
	return "nightlysignal: reading " + e.Path + ": " + e.Err.Error()
}

func (e *WorkflowParseError) Unwrap() error { return e.Err }

// APIError reports a non-2xx answer from the forge.
type APIError struct {
	// Method is the HTTP method.
	Method string
	// Route is the API path, without the host.
	Route string
	// Status is the HTTP status code.
	Status int
	// Body is the (possibly truncated) response body.
	Body string
}

func (e *APIError) Error() string {
	return "nightlysignal: " + e.Method + " " + e.Route + ": HTTP " +
		strconv.Itoa(e.Status) + ": " + e.Body
}

// TransportError reports a request that never produced an answer.
type TransportError struct {
	// Method is the HTTP method.
	Method string
	// Route is the API path, without the host.
	Route string
	// Err is the underlying failure.
	Err error
}

func (e *TransportError) Error() string {
	return "nightlysignal: " + e.Method + " " + e.Route + ": " + e.Err.Error()
}

func (e *TransportError) Unwrap() error { return e.Err }

// DecodeError reports a 2xx answer whose body is not the JSON expected.
type DecodeError struct {
	// Route is the API path that answered.
	Route string
	// Err is the underlying failure.
	Err error
}

func (e *DecodeError) Error() string {
	return "nightlysignal: decoding the answer from " + e.Route + ": " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error { return e.Err }

// EncodeError reports a request body that could not be marshalled.
type EncodeError struct {
	// Route is the API path the body was destined for.
	Route string
	// Err is the underlying failure.
	Err error
}

func (e *EncodeError) Error() string {
	return "nightlysignal: encoding the request to " + e.Route + ": " + e.Err.Error()
}

func (e *EncodeError) Unwrap() error { return e.Err }

// PaginationError reports that a listing did not end within the page cap.
type PaginationError struct {
	// Route names the listing.
	Route string
	// MaxPages is the cap that was hit.
	MaxPages int
	// Seen is how many records had been read.
	Seen int
}

func (e *PaginationError) Error() string {
	return "nightlysignal: the " + e.Route + " listing still had pages after " +
		strconv.Itoa(e.MaxPages) + " (" + strconv.Itoa(e.Seen) +
		" records read); stopping here would make an existing tracking issue look absent"
}

// DuplicateIssueError reports more than one open tracking issue carrying the
// same marker.
//
// A state this tool cannot create on its own, so finding it means something
// else did — a hand-copied body, or a race between two runs. Refused rather
// than resolved by picking one: updating an arbitrary member of the pair is how
// a duplicate becomes permanent.
type DuplicateIssueError struct {
	// Marker is the key both issues carry.
	Marker string
	// Indexes are the issue numbers that carry it.
	Indexes []int64
}

func (e *DuplicateIssueError) Error() string {
	numbers := make([]string, 0, len(e.Indexes))
	for _, n := range e.Indexes {
		numbers = append(numbers, "#"+strconv.FormatInt(n, 10))
	}
	sort.Strings(numbers)
	return "nightlysignal: " + strconv.Itoa(len(e.Indexes)) +
		" open issues carry the marker " + strconv.Quote(e.Marker) + " (" +
		strings.Join(numbers, ", ") + "); close all but one by hand"
}

// LabelError reports that the signal landed but could not be labelled.
//
// Deliberately NOT fatal at the point it happens: the issue is the signal and
// it already exists. It is fatal to the PROCESS, reported at the end, so the
// signal job goes red and someone fixes the label rather than the mechanism
// degrading quietly into an unlabelled issue nobody triages.
type LabelError struct {
	// Index is the issue that could not be labelled.
	Index int64
	// Labels are the names that were attempted.
	Labels []string
	// Err is the underlying failure.
	Err error
}

func (e *LabelError) Error() string {
	return "nightlysignal: issue #" + strconv.FormatInt(e.Index, 10) +
		" was opened but could not be labelled " + strings.Join(e.Labels, ", ") +
		": " + e.Err.Error()
}

func (e *LabelError) Unwrap() error { return e.Err }
