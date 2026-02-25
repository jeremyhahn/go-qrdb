// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package shamir

import "fmt"

// SplitError is returned when splitting a secret fails.
type SplitError struct {
	Reason string
}

func (e *SplitError) Error() string {
	return fmt.Sprintf("qrdb/shamir: split failed: %s", e.Reason)
}

// CombineError is returned when combining shares fails.
type CombineError struct {
	Reason string
}

func (e *CombineError) Error() string {
	return fmt.Sprintf("qrdb/shamir: combine failed: %s", e.Reason)
}

// ValidationError is returned when share validation fails.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("qrdb/shamir: validation failed for %s: %s", e.Field, e.Reason)
}
