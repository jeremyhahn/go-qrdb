package grpcweb

// Conformance to the pinned gRPC-Web protocol, docs/reference/grpc-web-protocol.md
// (grpc/grpc 5563edae14). Each test is named after the section it checks,
// and docs/api-server/engineering.md ("gRPC-Web conformance") maps every
// section to its tests. The
// sections that state nothing a server does (design goals, HTTP/2 stream-id
// and go-away, retries, keep-alive, bidi streaming, versioning, browser
// features) have no test, and that table says why.

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
)

// webFrame is one length-prefixed message of a gRPC-Web body.
type webFrame struct {
	flag    byte
	payload []byte
}

// parseWebFrames splits a decoded gRPC-Web body into its frames, failing the
// test on a truncated one.
func parseWebFrames(t *testing.T, body []byte) []webFrame {
	t.Helper()
	var frames []webFrame
	for len(body) > 0 {
		if len(body) < 5 {
			t.Fatalf("truncated frame header: %x", body)
		}
		n := int(binary.BigEndian.Uint32(body[1:5]))
		if len(body) < 5+n {
			t.Fatalf("truncated frame: want %d bytes, have %d", n, len(body)-5)
		}
		frames = append(frames, webFrame{flag: body[0], payload: body[5 : 5+n]})
		body = body[5+n:]
	}
	return frames
}

// parseTrailerBlock reads a trailer frame's payload as the HTTP/1 header
// block the protocol requires: "name: value" lines, each ended by CRLF, with
// no terminating empty line.
func parseTrailerBlock(t *testing.T, block []byte) http.Header {
	t.Helper()
	if !bytes.HasSuffix(block, []byte("\r\n")) || bytes.HasSuffix(block, []byte("\r\n\r\n")) {
		t.Fatalf("trailer block %q: want CRLF-ended lines and no terminating empty line", block)
	}
	fields := make(http.Header)
	for _, line := range strings.Split(strings.TrimSuffix(string(block), "\r\n"), "\r\n") {
		name, value, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("trailer line %q is not \"name: value\"", line)
		}
		if name != strings.ToLower(name) {
			t.Errorf("trailer name %q is not lower-case", name)
		}
		fields.Add(name, value)
	}
	return fields
}

// webResponse is the part of an *http.Response the conformance tests assert
// on, captured once the body has been read to EOF and closed, so no caller
// holds a response whose body it would have to close.
type webResponse struct {
	StatusCode int
	Proto      string
	ProtoMajor int
	Header     http.Header
	Trailer    http.Header
}

// webExchange posts one gRPC-Web request to a real grpc.Server behind the
// Handler and returns the response with its body read to EOF and closed.
func webExchange(t *testing.T, gs *grpc.Server, http2 bool, contentType, accept string, body []byte) (webResponse, []byte) {
	t.Helper()
	srv, client := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), http2, nil)
	resp := postWeb(t, client, srv.URL+"/grpc-web"+healthpb.Health_Check_FullMethodName, contentType, accept, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return webResponse{
		StatusCode: resp.StatusCode,
		Proto:      resp.Proto,
		ProtoMajor: resp.ProtoMajor,
		Header:     resp.Header,
		Trailer:    resp.Trailer,
	}, raw
}

// lastTrailer returns the trailer frame, which must be the body's last frame
// and its only trailer frame, decoded as a header block.
func lastTrailer(t *testing.T, frames []webFrame) http.Header {
	t.Helper()
	if len(frames) == 0 {
		t.Fatal("body has no frames, want a trailer frame")
	}
	for _, f := range frames[:len(frames)-1] {
		if f.flag&trailerFlag != 0 {
			t.Fatalf("trailer frame before the last frame: %+v", frames)
		}
	}
	last := frames[len(frames)-1]
	if last.flag != trailerFlag {
		t.Fatalf("last frame flag = %#x, want %#x (uncompressed trailer)", last.flag, trailerFlag)
	}
	return parseTrailerBlock(t, last.payload)
}

// Content-Type 1: application/grpc-web, with the message format defaulting to
// +proto when it is missing.
func TestConformance_ContentType_GRPCWebDefaultsToProto(t *testing.T) {
	for _, ct := range []string{"application/grpc-web", "application/grpc-web+proto", "Application/GRPC-Web+Proto"} {
		t.Run(ct, func(t *testing.T) {
			gs, _ := newBackend(t)
			resp, raw := webExchange(t, gs, false, ct, "", grpcFrame(t, &healthpb.HealthCheckRequest{}))
			if got := resp.Header.Get("Content-Type"); got != "application/grpc-web+proto" {
				t.Errorf("content type = %q, want application/grpc-web+proto", got)
			}
			frames := parseWebFrames(t, raw)
			if got := lastTrailer(t, frames).Get("grpc-status"); got != "0" {
				t.Fatalf("grpc-status = %q, want 0", got)
			}
			res := &healthpb.HealthCheckResponse{}
			if err := proto.Unmarshal(frames[0].payload, res); err != nil || res.GetStatus() != healthpb.HealthCheckResponse_SERVING {
				t.Errorf("response = %v (%v), want SERVING", res, err)
			}
		})
	}
}

// Content-Type 1: the message format the sender names is the one the gRPC
// server is asked to decode.
func TestConformance_ContentType_SubtypeReachesServer(t *testing.T) {
	for ct, want := range map[string]string{
		"application/grpc-web":            "application/grpc+proto",
		"application/grpc-web+proto":      "application/grpc+proto",
		"application/grpc-web+json":       "application/grpc+json",
		"application/grpc-web-text+proto": "application/grpc+proto",
	} {
		t.Run(ct, func(t *testing.T) {
			var got string
			h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Content-Type")
			}))
			req, err := http.NewRequest(http.MethodPost, PathPrefix+"a.B/C", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", ct)
			h.ServeHTTP(&fakeResponseWriter{header: make(http.Header)}, req)
			if got != want {
				t.Errorf("backend content type = %q, want %q", got, want)
			}
		})
	}
}

// Content-Type 2: application/grpc-web-text is the text-encoded stream of
// application/grpc-web, answered in kind.
func TestConformance_ContentType_GRPCWebText(t *testing.T) {
	for _, ct := range []string{"application/grpc-web-text", "application/grpc-web-text+proto"} {
		t.Run(ct, func(t *testing.T) {
			gs, _ := newBackend(t)
			body := []byte(base64.StdEncoding.EncodeToString(grpcFrame(t, &healthpb.HealthCheckRequest{})))
			resp, raw := webExchange(t, gs, false, ct, "", body)
			if got := resp.Header.Get("Content-Type"); got != "application/grpc-web-text+proto" {
				t.Errorf("content type = %q, want application/grpc-web-text+proto", got)
			}
			if got := lastTrailer(t, parseWebFrames(t, decodeTextBody(t, string(raw)))).Get("grpc-status"); got != "0" {
				t.Errorf("grpc-status = %q, want 0", got)
			}
		})
	}
}

// Content-Type: a request in any other media type is refused before the gRPC
// server sees it.
func TestConformance_ContentType_UnsupportedIsRefused(t *testing.T) {
	for _, ct := range []string{"application/grpc", "application/grpc+proto", "application/json", "application/grpc-web-texts"} {
		t.Run(ct, func(t *testing.T) {
			gs, obs := newBackend(t)
			resp, _ := webExchange(t, gs, false, ct, "", grpcFrame(t, &healthpb.HealthCheckRequest{}))
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Errorf("status = %d, want 415", resp.StatusCode)
			}
			if _, _, method := obs.snapshot(); method != "" {
				t.Errorf("gRPC server ran %q, want no call", method)
			}
		})
	}
}

// HTTP wire protocols 1 and 3: any HTTP version serves the call, and the end
// of the body closes the stream.
func TestConformance_WireProtocol_AnyHTTPVersion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
		major int
	}{{"http1.1", false, 1}, {"http2", true, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			gs, _ := newBackend(t)
			resp, raw := webExchange(t, gs, tc.http2, "application/grpc-web+proto", "", grpcFrame(t, &healthpb.HealthCheckRequest{}))
			if resp.ProtoMajor != tc.major {
				t.Fatalf("response protocol = %s, want HTTP/%d", resp.Proto, tc.major)
			}
			frames := parseWebFrames(t, raw)
			if len(frames) != 2 || frames[0].flag != 0 {
				t.Fatalf("frames = %+v, want one data frame then the trailer", frames)
			}
			if got := lastTrailer(t, frames).Get("grpc-status"); got != "0" {
				t.Errorf("grpc-status = %q, want 0", got)
			}
		})
	}
}

// HTTP wire protocols 2: request header names may be in any case over
// HTTP/1.1.
func TestConformance_WireProtocol_MixedCaseHeaderNames(t *testing.T) {
	gs, obs := newBackend(t)
	srv, client := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), false, nil)
	frame := grpcFrame(t, &healthpb.HealthCheckRequest{})
	conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), client.Transport.(*http.Transport).TLSClientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := "POST /grpc-web" + healthpb.Health_Check_FullMethodName + " HTTP/1.1\r\n" +
		"Host: db.example.com\r\n" +
		"CONTENT-TYPE: application/grpc-web+proto\r\n" +
		"x-GRPC-web: 1\r\n" +
		"X-User-AGENT: grpc-web-javascript/0.1\r\n" +
		"content-length: " + strconv.Itoa(len(frame)) + "\r\n" +
		"Connection: close\r\n\r\n"
	if _, err := conn.Write(append([]byte(request), frame...)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := lastTrailer(t, parseWebFrames(t, raw)).Get("grpc-status"); got != "0" {
		t.Errorf("grpc-status = %q, want 0", got)
	}
	if md, _, _ := obs.snapshot(); len(md.Get("x-user-agent")) != 1 {
		t.Errorf("x-user-agent metadata = %v, want one value", md.Get("x-user-agent"))
	}
}

// HTTP wire protocols 2: trailers in the last length-prefixed message use
// lower-case names, whatever case the server gave them.
func TestConformance_WireProtocol_TrailerNamesLowerCase(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Add("Trailer", "Grpc-Status, X-Declared-Trailer")
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Grpc-Status", "0")
		w.Header().Set("X-Declared-Trailer", "a")
		w.Header().Set(http.TrailerPrefix+"X-Undeclared-TRAILER", "b")
	}))
	req, err := http.NewRequest(http.MethodPost, PathPrefix+"a.B/C", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc-web")
	rec := &recordingWriter{fakeResponseWriter: fakeResponseWriter{header: make(http.Header)}}
	h.ServeHTTP(rec, req)
	frames := parseWebFrames(t, rec.body.Bytes())
	lastTrailer(t, frames)
	block := string(frames[len(frames)-1].payload)
	want := "grpc-status: 0\r\nx-declared-trailer: a\r\nx-undeclared-trailer: b\r\n"
	if block != want {
		t.Errorf("trailer block = %q, want %q", block, want)
	}
}

// Message framing 1 and 2: the status travels in the body as an uncompressed
// trailer frame (flag 0x80) holding an HTTP/1 header block, never as an HTTP
// header or HTTP trailer, and the HTTP status stays 200.
func TestConformance_MessageFraming_StatusInBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service string
		status  string
		message string
	}{{"ok", "", "0", ""}, {"error", deniedService, "7", "denied: missing permission"}} {
		t.Run(tc.name, func(t *testing.T) {
			gs, _ := newBackend(t)
			resp, raw := webExchange(t, gs, true, "application/grpc-web+proto", "", grpcFrame(t, &healthpb.HealthCheckRequest{Service: tc.service}))
			if resp.StatusCode != http.StatusOK {
				t.Errorf("HTTP status = %d, want 200", resp.StatusCode)
			}
			for _, name := range []string{"Grpc-Status", "Grpc-Message", "Trailer"} {
				if got := resp.Header.Values(name); len(got) != 0 {
					t.Errorf("HTTP header %s = %v, want none", name, got)
				}
			}
			if len(resp.Trailer) != 0 {
				t.Errorf("HTTP trailers = %v, want none", resp.Trailer)
			}
			fields := lastTrailer(t, parseWebFrames(t, raw))
			if got := fields.Get("grpc-status"); got != tc.status {
				t.Errorf("grpc-status = %q, want %q", got, tc.status)
			}
			if got := fields.Get("grpc-message"); got != tc.message {
				t.Errorf("grpc-message = %q, want %q", got, tc.message)
			}
		})
	}
}

// Message framing 2 and 3: data frames carry flag 0x00, and exactly one
// trailer frame ends the response, after every data frame.
func TestConformance_MessageFraming_TrailersLast(t *testing.T) {
	gs, _ := newBackend(t)
	_, raw := webExchange(t, gs, false, "application/grpc-web+proto", "", grpcFrame(t, &healthpb.HealthCheckRequest{}))
	frames := parseWebFrames(t, raw)
	lastTrailer(t, frames)
	for _, f := range frames[:len(frames)-1] {
		if f.flag != 0 {
			t.Errorf("data frame flag = %#x, want 0", f.flag)
		}
	}
}

// Message framing 4: a trailers-only response sends the trailers with the
// response headers and no message in the body.
func TestConformance_MessageFraming_TrailersOnly(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Grpc-Status", "14")
		w.Header().Set("Grpc-Message", "unavailable")
		w.WriteHeader(http.StatusOK)
	}))
	req, err := http.NewRequest(http.MethodPost, PathPrefix+"a.B/C", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc-web-text")
	rec := &recordingWriter{fakeResponseWriter: fakeResponseWriter{header: make(http.Header)}}
	h.ServeHTTP(rec, req)
	if rec.header.Get("Grpc-Status") != "14" || rec.header.Get("Grpc-Message") != "unavailable" {
		t.Errorf("headers = %v, want Grpc-Status 14 and Grpc-Message", rec.header)
	}
	if rec.body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.body.String())
	}
}

// User Agent: X-User-Agent, which a browser client sets in place of
// User-Agent, reaches the gRPC server as call metadata.
func TestConformance_UserAgent_XUserAgentReachesServer(t *testing.T) {
	gs, obs := newBackend(t)
	srv, client := newWebServer(t, mustHandler(t, Config{Enabled: true}, gs), true, nil)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/grpc-web"+healthpb.Health_Check_FullMethodName,
		bytes.NewReader(grpcFrame(t, &healthpb.HealthCheckRequest{})))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("X-User-Agent", "grpc-web-javascript/0.1")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if md, _, _ := obs.snapshot(); len(md.Get("x-user-agent")) != 1 || md.Get("x-user-agent")[0] != "grpc-web-javascript/0.1" {
		t.Errorf("x-user-agent metadata = %v, want [grpc-web-javascript/0.1]", md.Get("x-user-agent"))
	}
}

// Text-encoded streams 1: Accept: application/grpc-web-text asks for a text
// response to a binary request.
func TestConformance_TextStreams_AcceptSelectsText(t *testing.T) {
	gs, _ := newBackend(t)
	resp, raw := webExchange(t, gs, false, "application/grpc-web+proto", "application/grpc-web-text", grpcFrame(t, &healthpb.HealthCheckRequest{}))
	if got := resp.Header.Get("Content-Type"); got != "application/grpc-web-text+proto" {
		t.Fatalf("content type = %q, want application/grpc-web-text+proto", got)
	}
	if got := lastTrailer(t, parseWebFrames(t, decodeTextBody(t, string(raw)))).Get("grpc-status"); got != "0" {
		t.Errorf("grpc-status = %q, want 0", got)
	}
}

// Text-encoded streams 2: the encoding is base64 without a
// Content-Transfer-Encoding header, and the server encodes and flushes each
// message atomically, so every padded chunk ends on a message boundary.
func TestConformance_TextStreams_MessagesEncodedAtomically(t *testing.T) {
	gs, _ := newBackend(t)
	resp, raw := webExchange(t, gs, false, "application/grpc-web-text", "", []byte(base64.StdEncoding.EncodeToString(grpcFrame(t, &healthpb.HealthCheckRequest{}))))
	if got := resp.Header.Values("Content-Transfer-Encoding"); len(got) != 0 {
		t.Errorf("Content-Transfer-Encoding = %v, want none", got)
	}
	var decoded []byte
	for text := string(raw); text != ""; {
		end := strings.IndexByte(text, '=')
		if end < 0 {
			end = len(text)
		} else {
			end = min((end/4+1)*4, len(text))
		}
		chunk := text[:end]
		text = text[end:]
		part, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			t.Fatalf("chunk %q: %v", chunk, err)
		}
		decoded = append(decoded, part...)
		if n := completeFrames(decoded); n != len(decoded) {
			t.Errorf("chunk %q ends %d bytes into a message, want a message boundary", chunk, len(decoded)-n)
		}
	}
	lastTrailer(t, parseWebFrames(t, decoded))
}

// Text-encoded streams 2: a text request body made of separately padded
// base64 chunks is read as one stream.
func TestConformance_TextStreams_ChunkedRequestBody(t *testing.T) {
	frame := grpcFrame(t, &healthpb.HealthCheckRequest{Service: deniedService})
	body := base64.StdEncoding.EncodeToString(frame[:5]) + "\r\n" + base64.StdEncoding.EncodeToString(frame[5:6]) + base64.StdEncoding.EncodeToString(frame[6:])
	if !strings.Contains(strings.TrimRight(body, "="), "=") {
		t.Fatalf("body %q has no padding before its end", body)
	}
	gs, obs := newBackend(t)
	_, raw := webExchange(t, gs, false, "application/grpc-web-text+proto", "", []byte(body))
	if got := lastTrailer(t, parseWebFrames(t, decodeTextBody(t, string(raw)))).Get("grpc-status"); got != "7" {
		t.Errorf("grpc-status = %q, want 7 (the denied service reached the server)", got)
	}
	if _, _, method := obs.snapshot(); method != healthpb.Health_Check_FullMethodName {
		t.Errorf("method = %q, want %q", method, healthpb.Health_Check_FullMethodName)
	}
}

// Message framing, error paths: a request the gRPC server cannot read ends
// in a trailer frame with a non-OK status, under HTTP 200.
func TestConformance_MessageFraming_MalformedRequest(t *testing.T) {
	frame := grpcFrame(t, &healthpb.HealthCheckRequest{Service: "x"})
	trailerFlagged := append([]byte{trailerFlag}, frame[1:]...)
	for _, tc := range []struct {
		name        string
		contentType string
		body        []byte
		status      string
	}{
		{"truncated header", "application/grpc-web", frame[:3], "13"},
		{"truncated message", "application/grpc-web", frame[:len(frame)-1], "13"},
		{"trailer flag in request", "application/grpc-web", trailerFlagged, "13"},
		{"corrupt base64", "application/grpc-web-text", []byte("AAAA*AAA"), "14"},
		{"truncated base64", "application/grpc-web-text", []byte("AAAAAA"), "13"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gs, _ := newBackend(t)
			resp, raw := webExchange(t, gs, false, tc.contentType, "", tc.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("HTTP status = %d, want 200", resp.StatusCode)
			}
			if strings.HasPrefix(tc.contentType, webTextContentType) {
				raw = decodeTextBody(t, string(raw))
			}
			if got := lastTrailer(t, parseWebFrames(t, raw)).Get("grpc-status"); got != tc.status {
				t.Errorf("grpc-status = %q, want %q", got, tc.status)
			}
		})
	}
}

// recordingWriter is a fakeResponseWriter that keeps what is written to it.
type recordingWriter struct {
	fakeResponseWriter
	body bytes.Buffer
}

func (w *recordingWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
