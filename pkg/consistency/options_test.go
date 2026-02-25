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

package consistency

import "testing"

func TestDefaultReadOptionsConsistency(t *testing.T) {
	opts := DefaultReadOptions()
	if opts.Consistency != Linearizable {
		t.Errorf("DefaultReadOptions().Consistency = %v, want %v", opts.Consistency, Linearizable)
	}
}

func TestDefaultReadOptionsTimeout(t *testing.T) {
	opts := DefaultReadOptions()
	if opts.Timeout != 5000 {
		t.Errorf("DefaultReadOptions().Timeout = %d, want %d", opts.Timeout, 5000)
	}
}

func TestDefaultReadOptionsIsValid(t *testing.T) {
	opts := DefaultReadOptions()
	if err := opts.Validate(); err != nil {
		t.Errorf("DefaultReadOptions().Validate() returned unexpected error: %v", err)
	}
}

func TestValidateValidOptions(t *testing.T) {
	tests := []struct {
		name string
		opts ReadOptions
	}{
		{
			name: "strong with positive timeout",
			opts: ReadOptions{Consistency: Strong, Timeout: 1000},
		},
		{
			name: "linearizable with positive timeout",
			opts: ReadOptions{Consistency: Linearizable, Timeout: 5000},
		},
		{
			name: "eventual with positive timeout",
			opts: ReadOptions{Consistency: Eventual, Timeout: 3000},
		},
		{
			name: "zero timeout is valid",
			opts: ReadOptions{Consistency: Linearizable, Timeout: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.opts.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestValidateInvalidLevel(t *testing.T) {
	tests := []struct {
		name  string
		level Level
	}{
		{
			name:  "negative level",
			level: Level(-1),
		},
		{
			name:  "out of range level",
			level: Level(99),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := ReadOptions{Consistency: tt.level, Timeout: 1000}
			err := opts.Validate()
			if err == nil {
				t.Fatalf("Validate() expected error for invalid level %d, got nil", tt.level)
			}
			if _, ok := err.(*InvalidLevelError); !ok {
				t.Errorf("Validate() error type = %T, want *InvalidLevelError", err)
			}
		})
	}
}

func TestValidateNegativeTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout int64
	}{
		{
			name:    "negative one",
			timeout: -1,
		},
		{
			name:    "large negative",
			timeout: -9999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := ReadOptions{Consistency: Linearizable, Timeout: tt.timeout}
			err := opts.Validate()
			if err == nil {
				t.Fatalf("Validate() expected error for timeout %d, got nil", tt.timeout)
			}
			ite, ok := err.(*InvalidTimeoutError)
			if !ok {
				t.Fatalf("Validate() error type = %T, want *InvalidTimeoutError", err)
			}
			if ite.Timeout != tt.timeout {
				t.Errorf("InvalidTimeoutError.Timeout = %d, want %d", ite.Timeout, tt.timeout)
			}
		})
	}
}

func TestValidateInvalidLevelTakesPrecedenceOverNegativeTimeout(t *testing.T) {
	opts := ReadOptions{Consistency: Level(-1), Timeout: -100}
	err := opts.Validate()
	if err == nil {
		t.Fatal("Validate() expected error, got nil")
	}
	if _, ok := err.(*InvalidLevelError); !ok {
		t.Errorf("Validate() error type = %T, want *InvalidLevelError (level checked first)", err)
	}
}

func TestDefaultReadOptionsAllowStaleFalse(t *testing.T) {
	opts := DefaultReadOptions()
	if opts.AllowStale {
		t.Error("DefaultReadOptions().AllowStale = true, want false")
	}
}

func TestReadOptionsAllowStaleWithEventual(t *testing.T) {
	opts := ReadOptions{
		Consistency: Eventual,
		Timeout:     5000,
		AllowStale:  true,
	}
	if err := opts.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error for Eventual+AllowStale: %v", err)
	}
	if !opts.AllowStale {
		t.Error("AllowStale should be true")
	}
}

func TestReadOptionsAllowStaleIgnoredForStrong(t *testing.T) {
	opts := ReadOptions{
		Consistency: Strong,
		Timeout:     5000,
		AllowStale:  true,
	}
	if err := opts.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}
