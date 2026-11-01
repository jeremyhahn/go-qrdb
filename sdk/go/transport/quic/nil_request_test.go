// Copyright 2026 Jeremy Hahn
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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestFrameTransport_UpdatePoolRefusesANilRequest pins that a nil
// UpdatePool request is refused with a typed error and never framed.
func TestFrameTransport_UpdatePoolRefusesANilRequest(t *testing.T) {
	frames := newRecordingFrames()
	tr := NewFrameTransport("", frames.send)

	_, err := tr.UpdatePool(context.Background(), nil)
	var nre *transport.NilRequestError
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != "UpdatePool" || !errors.As(err, &nre) {
		t.Fatalf("UpdatePool(nil) = %T %v, want RequestError wrapping NilRequestError", err, err)
	}
	if len(frames.sent) != 0 {
		t.Fatalf("a nil request reached the wire: %v", frames.sent)
	}
}

// TestFrameTransport_UpdatePoolFramesARequest pins that the guard refuses
// only nil: a populated request is framed.
func TestFrameTransport_UpdatePoolFramesARequest(t *testing.T) {
	frames := newRecordingFrames()
	tr := NewFrameTransport("", frames.send)

	_, _ = tr.UpdatePool(context.Background(), &transport.UpdateVolumePoolRequest{Name: "p"})
	if len(frames.sent) != 1 {
		t.Fatalf("frames sent = %v, want one UpdatePool frame", frames.sent)
	}
}
