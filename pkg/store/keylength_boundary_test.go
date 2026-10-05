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

package store

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TestKeyLengthBoundary_PebbleAndMVCC is go-qrdb#592 F1's boundary test at
// the pkg/store level: both state machines decode kv.OpPut through
// go-quicraft's own kv.DecodeCommand (already int-safe) and MVCC puts
// through protocol.DecodeMVCCPut, now fixed centrally in pkg/protocol, so
// this is the regression pin for that shared fix rather than a duplicate
// of it.
func TestKeyLengthBoundary_PebbleAndMVCC(t *testing.T) {
	type stateMachine interface {
		Open(ctx context.Context, initialDir string, stopper <-chan struct{}) (uint64, error)
		Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
		Lookup(ctx context.Context, query interface{}) (interface{}, error)
	}
	builders := map[string]func() stateMachine{
		"pebble": func() stateMachine { return NewPebbleStateMachine(nil) },
		"mvcc":   func() stateMachine { return NewMVCCStateMachine(nil) },
	}
	lens := []int{65525, 65526, 65530, 65533, 65534, 65535}
	for name, build := range builders {
		for _, n := range lens {
			for _, enc := range []string{"kvput", "mvccput"} {
				t.Run(name+"/"+enc+"/"+itoaStoreBoundary(n), func(t *testing.T) {
					s := build()
					ctx := context.Background()
					if _, err := s.Open(ctx, t.TempDir(), nil); err != nil {
						t.Fatalf("Open: %v", err)
					}

					key := make([]byte, n)
					for i := range key {
						key[i] = 'k'
					}
					var cmd []byte
					var err error
					switch enc {
					case "kvput":
						cmd, err = kv.EncodePut(key, []byte("VAL"))
					case "mvccput":
						cmd, err = protocol.EncodeMVCCPut(key, []byte("VAL"), 0)
					}
					if err != nil {
						t.Fatalf("encode: %v", err)
					}

					func() {
						defer func() {
							if p := recover(); p != nil {
								t.Fatalf("n=%d: Apply panicked: %v", n, p)
							}
						}()
						res := make([]sm.Result, 1)
						if err := s.Update(ctx, []sm.Entry{{Index: 1, Cmd: cmd}}, res); err != nil {
							t.Fatalf("n=%d: Update returned a Go error, which halts Apply: %v", n, err)
						}
						if msg, refused := protocol.DecodeAppError(res[0].Data); refused {
							t.Fatalf("n=%d: a %d-byte key was refused: %s", n, n, msg)
						}
					}()

					got, err := s.Lookup(ctx, key)
					if err != nil {
						t.Fatalf("n=%d: Lookup: %v", n, err)
					}
					gb, _ := got.([]byte)
					if string(gb) != "VAL" {
						t.Errorf("n=%d: FINDING stored value is %q, want %q", n, gb, "VAL")
					}
				})
			}
		}
	}
}

func itoaStoreBoundary(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
