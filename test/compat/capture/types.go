package capture

import "net/http"

// CapturedResponse is the in-memory representation of a response from either
// drps or frps. It supports HTTP/1.1, chunked transfer, SSE, and WebSocket
// modes — the comparator switches on which fields are populated.
type CapturedResponse struct {
	// Status is the HTTP status code. For WebSocket, this is the upgrade response status (101).
	Status int

	// Headers is the response header map. Header keys are Go-canonicalized
	// (e.g. "Content-Type") — Go's net/http normalizes on read.
	Headers http.Header

	// Body is the full response body bytes for http/chunked modes.
	// For SSE, this is the raw event-stream bytes (parsed events live in SSEEvents).
	// For WebSocket, this is empty (frames live in WSFrames).
	Body []byte

	// ChunkedOnWire is true iff the response used Transfer-Encoding: chunked
	// (detected via httptrace or the response's TransferEncoding slice).
	ChunkedOnWire bool

	// SSEEvents is the parsed event list for mode=sse scenarios.
	SSEEvents []SSEEvent

	// WSFrames is the frame list (opcode + payload) for mode=websocket scenarios.
	WSFrames []WSFrame

	// Err is a capture-time error. If non-nil, comparison is skipped and
	// the test fails with this error as the cause.
	Err error
}

// SSEEvent is one dispatched Server-Sent Event per WHATWG spec.
type SSEEvent struct {
	Event string // "" if not specified (default "message" semantics, but stored as-is)
	Data  string // multi-line data joined with "\n"
	ID    string // last-event-id; may persist across events
}

// WSFrame is a WebSocket frame for comparison.
type WSFrame struct {
	Opcode  byte
	Payload []byte
}
