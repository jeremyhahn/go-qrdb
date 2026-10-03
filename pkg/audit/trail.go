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

package audit

import "path/filepath"

// DefaultTrailFile is the compliance trail's file name under a node's data
// directory: qrdbd's, and the desktop embedded server's when it keeps one.
const DefaultTrailFile = "audit-events.ndjson"

// TrailRingSize is how many recent events a node's compliance trail keeps in
// memory for queries.
const TrailRingSize = 10000

// OpenTrail opens a node's compliance trail: the hash-chained file at path,
// an in-memory ring of the most recent TrailRingSize events, and the
// operational log, fanned out by one aggregate. It is the one composition
// every node composer -- the daemon, the desktop's embedded server -- opens,
// so their trails are the same thing. The file is opened, or created, here:
// a path that cannot be is returned as the file logger's error and nothing
// is opened. The file is confined to path's directory (NewFileLogger).
func OpenTrail(path string) (AuditLogger, error) {
	return OpenTrailIn(filepath.Dir(path), filepath.Base(path))
}

// OpenTrailIn is OpenTrail for the file name below dir, a node's data
// directory, confined to dir (NewFileLoggerIn): a symbolic link in the data
// directory -- at name, or at a directory on its path -- that resolves
// outside it refuses the trail rather than carrying it there.
func OpenTrailIn(dir, name string) (AuditLogger, error) {
	chain, err := NewFileLoggerIn(dir, name)
	if err != nil {
		return nil, err
	}
	return NewAggregateLogger(chain, NewRingBuffer(TrailRingSize), NewSlogSink()), nil
}
