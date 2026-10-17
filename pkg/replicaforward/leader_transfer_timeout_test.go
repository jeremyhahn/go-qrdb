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

package replicaforward

import (
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestRefusal_LeaderTransferTimeoutRoundTrips(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 7, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}

	// Wrapped, as a transport handler may receive it.
	reason, md, ok := Refusal(fmt.Errorf("transfer: %w", want))
	if !ok || reason != ReasonLeaderTransferTimeout {
		t.Fatalf("Refusal = (%q, ok=%v), want %s", reason, ok, ReasonLeaderTransferTimeout)
	}
	got, err := ParseLeaderTransferTimeout(md)
	if err != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", err)
	}
	if *got != *want {
		t.Fatalf("round trip = %+v, want %+v", *got, *want)
	}
}

func TestRefusal_LeaderTransferTimeoutAwayFromLeader(t *testing.T) {
	// ToReplicaID 0 is a transfer that only had to move leadership away.
	want := &service.LeaderTransferTimeoutError{ShardID: 7, FromReplicaID: 2, Timeout: 1500 * time.Millisecond}
	got, err := ParseLeaderTransferTimeout(LeaderTransferTimeoutMetadata(want))
	if err != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", err)
	}
	if *got != *want {
		t.Fatalf("round trip = %+v, want %+v", *got, *want)
	}
}

func TestParseLeaderTransferTimeout_RejectsMalformedMetadata(t *testing.T) {
	valid := LeaderTransferTimeoutMetadata(&service.LeaderTransferTimeoutError{
		ShardID: 7, FromReplicaID: 1, ToReplicaID: 3, Timeout: time.Second,
	})
	overflow := strconv.FormatUint(uint64(1)<<63, 10)

	cases := map[string]struct {
		key, value string
		drop       bool
	}{
		"missing shard":         {key: keyShard, drop: true},
		"missing from":          {key: keyFromReplicaID, drop: true},
		"missing to":            {key: keyToReplicaID, drop: true},
		"missing timeout":       {key: keyTimeoutMillis, drop: true},
		"non-numeric shard":     {key: keyShard, value: "seven"},
		"negative timeout":      {key: keyTimeoutMillis, value: "-1"},
		"timeout overflows int": {key: keyTimeoutMillis, value: overflow},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			md := make(map[string]string, len(valid))
			for k, v := range valid {
				md[k] = v
			}
			if tc.drop {
				delete(md, tc.key)
			} else {
				md[tc.key] = tc.value
			}

			_, err := ParseLeaderTransferTimeout(md)
			var decodeErr *RefusalDecodeError
			if !errors.As(err, &decodeErr) || decodeErr.Key != tc.key {
				t.Fatalf("err = %v, want *RefusalDecodeError naming %s", err, tc.key)
			}
		})
	}
}
