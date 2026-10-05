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

package rest

import (
	"net/http"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// parseConsistencyParam reads the ?consistency= query parameter from q and
// returns the resolved ReadOptions. The second return value is true when an
// explicit consistency parameter was provided (and the caller should use the
// WithConsistency service variant instead of the plain one).
//
// On an unrecognised ?consistency= value the helper writes an HTTP 400 to w
// and returns (_, false, false) — the caller must check the third return value
// and return immediately when it is false.
func parseConsistencyParam(w http.ResponseWriter, q url.Values) (opts consistency.ReadOptions, specified bool, ok bool) {
	consistencyStr := q.Get("consistency")

	if consistencyStr == "" {
		// Absent: caller uses plain service method.
		return consistency.ReadOptions{}, false, true
	}

	level, err := consistency.ParseLevel(consistencyStr)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "consistency", Err: err})
		return consistency.ReadOptions{}, false, false
	}

	opts = consistency.ReadOptions{
		Consistency: level,
		AllowStale:  level == consistency.Eventual,
	}
	return opts, true, true
}

// parseWriteModeParam reads the ?write_mode= query parameter from q and
// returns the resolved Mode. The second return value is true when an explicit
// write_mode was provided (and the caller should use the WithMode service
// variant). On an unrecognised value the helper writes HTTP 400 and returns
// (_, false, false) — the caller must check the third value and return
// immediately when false.
func parseWriteModeParam(w http.ResponseWriter, q url.Values) (mode writemode.Mode, specified bool, ok bool) {
	modeStr := q.Get("write_mode")
	if modeStr == "" {
		return writemode.Sync, false, true
	}

	m, err := writemode.FromString(modeStr)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "write_mode", Err: err})
		return writemode.Sync, false, false
	}

	return m, true, true
}
