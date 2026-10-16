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

package storage

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestValidateRange(t *testing.T) {
	cases := map[string]struct {
		start, end []byte
		want       error
	}{
		"bounded":           {[]byte("a"), []byte("b"), nil},
		"nil end unbounded": {[]byte("a"), nil, nil},
		"empty end":         {[]byte("a"), []byte{}, nil},
		"nil start":         {nil, []byte("b"), ErrEmptyKey},
		"empty start":       {[]byte{}, nil, ErrEmptyKey},
		"end equals start":  {[]byte("a"), []byte("a"), ErrInvalidRange},
		"end before start":  {[]byte("b"), []byte("a"), ErrInvalidRange},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ValidateRange(tc.start, tc.end); !errors.Is(got, tc.want) {
				t.Fatalf("ValidateRange(%q, %q) = %v, want %v", tc.start, tc.end, got, tc.want)
			}
		})
	}
}

func TestPrefixEnd(t *testing.T) {
	cases := map[string]struct {
		prefix, want []byte
	}{
		"increments last byte":  {[]byte("ab"), []byte("ac")},
		"carries past 0xff":     {[]byte{'a', 0xff}, []byte("b")},
		"carries past all 0xff": {[]byte{'a', 0xff, 0xff}, []byte("b")},
		"all 0xff is unbounded": {[]byte{0xff, 0xff}, nil},
		"empty is unbounded":    {nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := PrefixEnd(tc.prefix); !bytes.Equal(got, tc.want) || (tc.want == nil) != (got == nil) {
				t.Fatalf("PrefixEnd(%q) = %q, want %q", tc.prefix, got, tc.want)
			}
		})
	}
}

// TestPrefixEnd_BoundsEveryKeyWithThePrefix checks the property callers rely
// on: every key carrying the prefix sorts below the end, and the prefix's
// successor does not.
func TestPrefixEnd_BoundsEveryKeyWithThePrefix(t *testing.T) {
	prefix := []byte("db/")
	end := PrefixEnd(prefix)
	for _, k := range []string{"db/", "db/a", "db/\xff\xff"} {
		if !inRange([]byte(k), prefix, end) {
			t.Errorf("%q is outside [%q, %q)", k, prefix, end)
		}
	}
	for _, k := range []string{"db0", "dc", "db"} {
		if inRange([]byte(k), prefix, end) {
			t.Errorf("%q is inside [%q, %q)", k, prefix, end)
		}
	}
	if !bytes.Equal(prefix, []byte("db/")) {
		t.Fatalf("PrefixEnd modified its input: %q", prefix)
	}
}

func TestInRange(t *testing.T) {
	cases := map[string]struct {
		key  string
		end  []byte
		want bool
	}{
		"at start":        {"b", []byte("d"), true},
		"inside":          {"c", []byte("d"), true},
		"at end":          {"d", []byte("d"), false},
		"before start":    {"a", []byte("d"), false},
		"unbounded above": {"zzz", nil, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := inRange([]byte(tc.key), []byte("b"), tc.end); got != tc.want {
				t.Fatalf("inRange(%q, b, %q) = %v, want %v", tc.key, tc.end, got, tc.want)
			}
		})
	}
}

func TestBufferedBatch_DeleteRangeRefusals(t *testing.T) {
	base := newMapReader(nil)

	b := NewBufferedBatch(base, base.apply)
	err := b.DeleteRange([]byte("b"), []byte("a"))
	var batchErr *BatchError
	if !errors.As(err, &batchErr) || batchErr.Op != "delete_range" || !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("DeleteRange(b, a) = %v, want a delete_range BatchError over ErrInvalidRange", err)
	}
	if err := b.DeleteRange(nil, []byte("a")); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("DeleteRange(nil, a) = %v, want ErrEmptyKey", err)
	}
	if b.Len() != 0 {
		t.Fatalf("refused range deletes were buffered: Len = %d", b.Len())
	}

	_ = b.Close()
	if err := b.DeleteRange([]byte("a"), []byte("b")); !errors.Is(err, ErrStorageClosed) {
		t.Fatalf("DeleteRange after Close = %v, want ErrStorageClosed", err)
	}
}

// TestBufferedBatch_GetHonoursRangeDeleteOrder pins the ordering contract: a
// range delete hides committed keys and keys written before it in the batch,
// leaves its exclusive end alone, and does not hide a key written after it.
func TestBufferedBatch_GetHonoursRangeDeleteOrder(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"})
	b := NewBufferedBatch(base, base.apply)

	mustNil(t, b.Set([]byte("bb"), []byte("pending")))
	mustNil(t, b.DeleteRange([]byte("b"), []byte("d")))
	mustNil(t, b.Set([]byte("c"), []byte("after")))

	want := map[string][]byte{"a": []byte("1"), "b": nil, "bb": nil, "c": []byte("after"), "d": []byte("4")}
	for k, v := range want {
		got, err := b.Get([]byte(k))
		mustNil(t, err)
		if !bytes.Equal(got, v) || (v == nil) != (got == nil) {
			t.Errorf("Get(%q) = %q, want %q", k, got, v)
		}
	}
}

func TestBufferedBatch_GetUnboundedRangeDelete(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "m": "2", "z": "3"})
	b := NewBufferedBatch(base, base.apply)
	mustNil(t, b.DeleteRange([]byte("m"), nil))

	for k, v := range map[string][]byte{"a": []byte("1"), "m": nil, "z": nil} {
		got, err := b.Get([]byte(k))
		mustNil(t, err)
		if !bytes.Equal(got, v) || (v == nil) != (got == nil) {
			t.Errorf("Get(%q) = %q, want %q", k, got, v)
		}
	}
}

// TestBufferedBatch_GetWithSeveralRangeDeletes checks Get weighs a key's latest
// write against every range delete buffered after it, and only those: a write
// between two range deletes is hidden by the later one, a write after both is
// not, and a single delete after a range still reads as deleted.
func TestBufferedBatch_GetWithSeveralRangeDeletes(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "k": "2", "q": "3", "z": "4"})
	b := NewBufferedBatch(base, base.apply)

	mustNil(t, b.DeleteRange([]byte("a"), []byte("c")))
	mustNil(t, b.Set([]byte("k"), []byte("between")))
	mustNil(t, b.Set([]byte("q"), []byte("between")))
	mustNil(t, b.DeleteRange([]byte("j"), []byte("m")))
	mustNil(t, b.Set([]byte("a"), []byte("after")))
	mustNil(t, b.Delete([]byte("z")))

	want := map[string][]byte{"a": []byte("after"), "k": nil, "q": []byte("between"), "z": nil}
	for k, v := range want {
		got, err := b.Get([]byte(k))
		mustNil(t, err)
		if !bytes.Equal(got, v) || (v == nil) != (got == nil) {
			t.Errorf("Get(%q) = %q, want %q", k, got, v)
		}
	}
}

func TestBufferedBatch_IteratorHonoursRangeDelete(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"})
	b := NewBufferedBatch(base, base.apply)

	mustNil(t, b.Set([]byte("a"), []byte("first")))
	mustNil(t, b.Set([]byte("a"), []byte("second")))
	mustNil(t, b.Set([]byte("bb"), []byte("hidden")))
	mustNil(t, b.Set([]byte("e"), []byte("kept")))
	mustNil(t, b.Set([]byte("e2"), []byte("gone")))
	mustNil(t, b.Delete([]byte("e2")))
	mustNil(t, b.DeleteRange([]byte("b"), []byte("d")))
	mustNil(t, b.Set([]byte("c"), []byte("after")))
	mustNil(t, b.Set([]byte("cc"), []byte("new")))

	got := collect(t, b.NewIterator(nil))
	want := []string{"a=second", "c=after", "cc=new", "d=4", "e=kept"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forward = %v, want %v", got, want)
	}

	it := b.NewIterator(&IteratorOptions{Reverse: true, OnlyKeys: true})
	var keys []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		if it.Value() != nil {
			t.Fatalf("OnlyKeys iterator returned a value for %q", it.Key())
		}
		keys = append(keys, string(it.Key()))
	}
	_ = it.Close()
	if want := []string{"e", "d", "cc", "c", "a"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("reverse keys = %v, want %v", keys, want)
	}
}

// TestBufferedBatch_CommitAppliesRangeDeleteInOrder checks the range op
// reaches the ApplyFunc with its bounds and in the order it was buffered,
// which is what lets a backend apply it atomically with its neighbours.
func TestBufferedBatch_CommitAppliesRangeDeleteInOrder(t *testing.T) {
	base := newMapReader(map[string]string{"a": "1", "b": "2", "c": "3"})
	var applied []BufferedOp
	b := NewBufferedBatch(base, func(ops []BufferedOp) error {
		applied = ops
		return base.apply(ops)
	})

	mustNil(t, b.Set([]byte("bb"), []byte("x")))
	mustNil(t, b.DeleteRange([]byte("b"), []byte("c")))
	mustNil(t, b.Set([]byte("b"), []byte("after")))
	mustNil(t, b.Commit())

	if len(applied) != 3 || !applied[1].Range || !applied[1].Deleted ||
		string(applied[1].Key) != "b" || string(applied[1].RangeEnd) != "c" {
		t.Fatalf("applied ops = %+v, want the range delete second with bounds [b, c)", applied)
	}
	want := map[string]string{"a": "1", "b": "after", "c": "3"}
	if !reflect.DeepEqual(base.data, want) {
		t.Fatalf("committed = %v, want %v", base.data, want)
	}
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
