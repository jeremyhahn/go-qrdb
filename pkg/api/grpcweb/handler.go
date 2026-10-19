package grpcweb

import (
	"encoding/base64"
	"encoding/binary"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
)

// Media types of the protocol (grpc-web-protocol.md, "Content-Type").
const (
	grpcContentType    = "application/grpc"
	webContentType     = "application/grpc-web"
	webTextContentType = "application/grpc-web-text"
	defaultSubtype     = "proto"
)

// trailerFlag marks an uncompressed trailer frame: the most significant bit
// of a frame's first byte (grpc-web-protocol.md, "Message framing").
const trailerFlag byte = 0x80

// webMediaTypes maps a gRPC-Web media type, without its message-format
// suffix, to whether it is the base64 text encoding.
var webMediaTypes = map[string]bool{
	webContentType:     false,
	webTextContentType: true,
}

// corsAllowMethods and corsExposeHeaders are the CORS response values: a
// gRPC-Web call is always a POST, and a trailers-only response carries the
// status in headers the browser must let the client read.
const (
	corsAllowMethods  = http.MethodPost
	corsExposeHeaders = "Grpc-Status, Grpc-Message, Grpc-Status-Details-Bin"
)

// Handler serves gRPC-Web requests under PathPrefix by translating them for
// a native gRPC http.Handler.
type Handler struct {
	backend          http.Handler
	origins          map[string]struct{}
	anyOrigin        bool
	allowCredentials bool
}

// NewHandler returns a Handler translating for backend, which must serve
// native gRPC over net/http, as (*grpc.Server).ServeHTTP does. It returns the
// error Config.Validate reports for cfg.
func NewHandler(cfg Config, backend http.Handler) (*Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	h := &Handler{
		backend:          backend,
		origins:          make(map[string]struct{}, len(cfg.AllowedOrigins)),
		allowCredentials: cfg.AllowCredentials,
	}
	for _, origin := range cfg.AllowedOrigins {
		if origin == wildcardOrigin {
			h.anyOrigin = true
			continue
		}
		h.origins[origin] = struct{}{}
	}
	return h, nil
}

// ServeHTTP serves one gRPC-Web request. Its path must begin with
// PathPrefix; the rest of the path is the gRPC method.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowedOrigin := h.cors(w, r)
	if r.Method == http.MethodOptions {
		h.preflight(w, r, allowedOrigin)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "grpc-web: method not allowed", http.StatusMethodNotAllowed)
		return
	}
	subtype, textRequest, ok := parseWebContentType(r.Header.Get("Content-Type"))
	if !ok {
		http.Error(w, "grpc-web: unsupported content type", http.StatusUnsupportedMediaType)
		return
	}
	textResponse := textRequest || acceptsText(r.Header.Values("Accept"))

	req := r.Clone(r.Context())
	req.ProtoMajor, req.ProtoMinor, req.Proto = 2, 0, "HTTP/2.0"
	req.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, PathPrefix)
	req.URL.RawPath = ""
	req.RequestURI = ""
	req.Header.Set("Content-Type", grpcContentType+"+"+subtype)
	if textRequest {
		req.Body = io.NopCloser(base64.NewDecoder(base64.StdEncoding, r.Body))
		req.ContentLength = -1
		req.Header.Del("Content-Length")
	}

	responseType := webContentType + "+" + subtype
	if textResponse {
		responseType = webTextContentType + "+" + subtype
	}
	rw := &responseWriter{
		w:           w,
		rc:          http.NewResponseController(w),
		header:      make(http.Header),
		contentType: responseType,
		text:        textResponse,
	}
	h.backend.ServeHTTP(rw, req)
	rw.finish()
}

// cors sets the CORS response headers for an admitted origin and reports
// whether the request's origin is admitted. A request without an Origin
// header is not a CORS request and gets no CORS headers.
func (h *Handler) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	w.Header().Add("Vary", "Origin")
	_, listed := h.origins[origin]
	if !listed && !h.anyOrigin {
		return false
	}
	switch {
	case h.allowCredentials:
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	case listed:
		w.Header().Set("Access-Control-Allow-Origin", origin)
	default:
		w.Header().Set("Access-Control-Allow-Origin", wildcardOrigin)
	}
	w.Header().Set("Access-Control-Expose-Headers", corsExposeHeaders)
	return true
}

// preflight answers a CORS preflight. An admitted origin asking for POST is
// granted the request headers it names, since gRPC metadata travels as
// arbitrary headers; anything else is refused.
func (h *Handler) preflight(w http.ResponseWriter, r *http.Request, allowedOrigin bool) {
	if !allowedOrigin || r.Header.Get("Access-Control-Request-Method") != http.MethodPost {
		http.Error(w, "grpc-web: cross-origin request refused", http.StatusForbidden)
		return
	}
	w.Header().Add("Vary", "Access-Control-Request-Method")
	w.Header().Add("Vary", "Access-Control-Request-Headers")
	w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
	if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
		w.Header().Set("Access-Control-Allow-Headers", requested)
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseWebContentType returns the message format named by a gRPC-Web
// Content-Type ("proto" when it names none) and whether the body is base64
// text. ok is false for any other media type.
func parseWebContentType(contentType string) (subtype string, text, ok bool) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false, false
	}
	base, subtype, _ := strings.Cut(mediaType, "+")
	text, ok = webMediaTypes[base]
	if !ok {
		return "", false, false
	}
	if subtype == "" {
		subtype = defaultSubtype
	}
	return subtype, text, true
}

// acceptsText reports whether an Accept header asks for the text encoding
// (grpc-web-protocol.md, "Text-encoded (response) streams").
func acceptsText(accept []string) bool {
	for _, value := range accept {
		for _, entry := range strings.Split(value, ",") {
			mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(entry))
			if err != nil {
				continue
			}
			if base, _, _ := strings.Cut(mediaType, "+"); base == webTextContentType {
				return true
			}
		}
	}
	return false
}

// responseWriter is the http.ResponseWriter the gRPC backend writes to. It
// forwards headers and data frames, and holds back the HTTP trailers the
// backend sets so finish can send them as the body's trailer frame. The
// backend writes from the goroutine running its ServeHTTP only, so it needs
// no lock.
type responseWriter struct {
	w           http.ResponseWriter
	rc          *http.ResponseController
	header      http.Header
	contentType string
	text        bool
	wroteHeader bool
	grpc        bool
	trailers    []string
}

var _ http.Flusher = (*responseWriter)(nil)

func (rw *responseWriter) Header() http.Header { return rw.header }

// WriteHeader sends the backend's headers, minus its trailer declarations,
// which it records. A gRPC response is relabelled with the gRPC-Web media
// type; anything else, such as a plain-text HTTP error, passes as written.
func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.wroteHeader = true
	dst := rw.w.Header()
	for key, values := range rw.header {
		switch {
		case key == "Trailer":
			for _, value := range values {
				for _, name := range strings.Split(value, ",") {
					rw.trailers = append(rw.trailers, http.CanonicalHeaderKey(strings.TrimSpace(name)))
				}
			}
		case strings.HasPrefix(key, http.TrailerPrefix):
		default:
			dst[key] = append([]string(nil), values...)
		}
	}
	if strings.HasPrefix(dst.Get("Content-Type"), grpcContentType) {
		rw.grpc = true
		dst.Set("Content-Type", rw.contentType)
		dst.Del("Content-Length")
	}
	rw.text = rw.text && rw.grpc
	rw.w.WriteHeader(code)
}

// Write sends one or more gRPC data frames, unchanged in the binary encoding
// and base64-encoded, padding included, in the text encoding.
func (rw *responseWriter) Write(p []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	if rw.text {
		if _, err := io.WriteString(rw.w, base64.StdEncoding.EncodeToString(p)); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return rw.w.Write(p)
}

// Flush flushes the client connection. A writer that cannot flush buffers
// until the handler returns, which delays a stream without breaking it.
func (rw *responseWriter) Flush() {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	_ = rw.rc.Flush()
}

// finish sends the trailers the backend set after its headers as the
// response's final, trailer frame: lowercase "name: value" lines, each
// ending CRLF, in sorted order.
func (rw *responseWriter) finish() {
	if !rw.grpc {
		return
	}
	fields := make(map[string][]string)
	for _, name := range rw.trailers {
		if values, ok := rw.header[name]; ok {
			fields[strings.ToLower(name)] = append(fields[strings.ToLower(name)], values...)
		}
	}
	for key, values := range rw.header {
		if name, ok := strings.CutPrefix(key, http.TrailerPrefix); ok {
			fields[strings.ToLower(name)] = append(fields[strings.ToLower(name)], values...)
		}
	}
	if len(fields) == 0 {
		return
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	var block strings.Builder
	for _, name := range names {
		for _, value := range fields[name] {
			block.WriteString(name)
			block.WriteString(": ")
			block.WriteString(value)
			block.WriteString("\r\n")
		}
	}
	frame := make([]byte, 5, 5+block.Len())
	frame[0] = trailerFlag
	binary.BigEndian.PutUint32(frame[1:], uint32(block.Len()))
	frame = append(frame, block.String()...)
	if _, err := rw.Write(frame); err != nil {
		return
	}
	_ = rw.rc.Flush()
}
