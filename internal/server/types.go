// Package server wires the serial package to MCP tools.
package server

import "github.com/wltbagent/agent-serial/internal/serial"

// ListPortsArgs takes no arguments.
type ListPortsArgs struct{}

type ListPortsResult struct {
	Ports []serial.PortInfo `json:"ports"`
}

type OpenArgs struct {
	Transport string `json:"transport,omitempty" jsonschema:"serial (default), telnet or ssh"`
	Port      string `json:"port" jsonschema:"device path (serial, e.g. /dev/ttyUSB0) or host:port (telnet defaults :23, ssh defaults :22)"`

	// Serial-only line settings; ignored for telnet/ssh.
	BaudRate    int    `json:"baud_rate,omitempty" jsonschema:"serial only: bits per second, e.g. 115200 (console default)"`
	DataBits    int    `json:"data_bits,omitempty" jsonschema:"serial only: 5-8, default 8"`
	StopBits    int    `json:"stop_bits,omitempty" jsonschema:"serial only: 1 or 2, default 1"`
	Parity      string `json:"parity,omitempty" jsonschema:"serial only: none (default), odd or even"`
	FlowControl string `json:"flow_control,omitempty" jsonschema:"serial only: only 'none' is supported"`

	// SSH-only fields.
	Username   string `json:"username,omitempty" jsonschema:"ssh only: login user"`
	Password   string `json:"password,omitempty" jsonschema:"ssh only: password (note: tool arguments may appear in logs; prefer key_path)"`
	KeyPath    string `json:"key_path,omitempty" jsonschema:"ssh only: path to a private key file (preferred over password)"`
	Passphrase string `json:"passphrase,omitempty" jsonschema:"ssh only: passphrase for an encrypted key file"`
	Command    string `json:"command,omitempty" jsonschema:"ssh only: run this remote command instead of a shell (console servers that expose a line via a command)"`
}

type OpenResult struct {
	ConnectionID string `json:"connection_id"`
	Transport    string `json:"transport"`
	Target       string `json:"target"`
	Config       string `json:"config"`
}

type WriteArgs struct {
	ConnectionID string `json:"connection_id" jsonschema:"id returned by open"`
	Data         string `json:"data" jsonschema:"payload to send; append the console line ending yourself (e.g. \"help\\n\")"`
	Encoding     string `json:"encoding,omitempty" jsonschema:"utf8 (default), hex or base64"`
}

type WriteResult struct {
	ConnectionID string `json:"connection_id"`
	BytesWritten int    `json:"bytes_written"`
}

type ReadArgs struct {
	ConnectionID     string `json:"connection_id" jsonschema:"id returned by open"`
	Encoding         string `json:"encoding,omitempty" jsonschema:"utf8 (default), hex or base64"`
	MaxBytes         int    `json:"max_bytes,omitempty" jsonschema:"cap on bytes returned, default 65536"`
	DurationMs       int64  `json:"duration_ms,omitempty" jsonschema:"capture window in ms; 0 or omitted returns buffered output immediately"`
	InitialTimeoutMs int64  `json:"initial_timeout_ms,omitempty" jsonschema:"with duration_ms: max wait for the first byte, defaults to the full window"`
	IdleTimeoutMs    int64  `json:"idle_timeout_ms,omitempty" jsonschema:"with duration_ms: stop this many ms after the most recent byte"`
	Peek             bool   `json:"peek,omitempty" jsonschema:"true: return buffered bytes without consuming them (immediate reads only)"`
}

type ReadResult struct {
	ConnectionID     string      `json:"connection_id"`
	Data             string      `json:"data"`
	BytesRead        int         `json:"bytes_read"`
	Encoding         string      `json:"encoding"`
	CompletionReason string      `json:"completion_reason"`
	WaitedMs         int64       `json:"waited_ms,omitempty"`
	ElapsedMs        int64       `json:"elapsed_ms,omitempty"`
	DroppedBytes     uint64      `json:"dropped_bytes,omitempty"`
	Chunks           []ReadChunk `json:"chunks,omitempty"`
}

// ReadChunk records where a burst of bytes landed within the capture. The
// concatenated payload is in Data; chunks only carry boundaries.
type ReadChunk struct {
	Offset int `json:"offset"`
	Bytes  int `json:"bytes"`
}

type ExpectArgs struct {
	ConnectionID     string `json:"connection_id" jsonschema:"id returned by open"`
	Pattern          string `json:"pattern" jsonschema:"substring to wait for in the device output, e.g. \"autoboot\""`
	Send             string `json:"send,omitempty" jsonschema:"payload written once per match, e.g. hex \"03\" for Ctrl-C; omit for wait-only"`
	SendEncoding     string `json:"send_encoding,omitempty" jsonschema:"encoding of send: utf8 (default), hex, base64"`
	Encoding         string `json:"encoding,omitempty" jsonschema:"encoding of returned data: utf8 (default), hex, base64"`
	MaxHits          int    `json:"max_hits,omitempty" jsonschema:"stop after this many matches, default 1; keeps recurring patterns (boot loops) from re-triggering"`
	DurationMs       int64  `json:"duration_ms,omitempty" jsonschema:"collect window after the first byte, default 10000"`
	InitialTimeoutMs int64  `json:"initial_timeout_ms,omitempty" jsonschema:"max wait for the first byte, defaults to duration_ms"`
	IdleTimeoutMs    int64  `json:"idle_timeout_ms,omitempty" jsonschema:"stop this many ms after the most recent byte if the pattern never appears"`
	SendDelayMs      int64  `json:"send_delay_ms,omitempty" jsonschema:"wait this many ms after a match before sending"`
	MaxBytes         int    `json:"max_bytes,omitempty" jsonschema:"cap on collected output bytes, default 65536"`
}

type ExpectResult struct {
	ConnectionID     string `json:"connection_id"`
	Data             string `json:"data"`
	BytesRead        int    `json:"bytes_read"`
	Encoding         string `json:"encoding"`
	CompletionReason string `json:"completion_reason"`
	Hits             int    `json:"hits"`
	Sent             int    `json:"sent_bytes"`
	MatchOffsets     []int  `json:"match_offsets,omitempty"`
	WaitedMs         int64  `json:"waited_ms,omitempty"`
	ElapsedMs        int64  `json:"elapsed_ms,omitempty"`
	DroppedBytes     uint64 `json:"dropped_bytes,omitempty"`
}

type ControlLinesArgs struct {
	ConnectionID string `json:"connection_id" jsonschema:"id returned by open"`
	RTS          *bool  `json:"rts,omitempty" jsonschema:"request-to-send level"`
	DTR          *bool  `json:"dtr,omitempty" jsonschema:"data-terminal-ready level; toggling DTR resets many boards"`
}

type ControlLinesResult struct {
	ConnectionID string `json:"connection_id"`
	RTS          *bool  `json:"rts,omitempty"`
	DTR          *bool  `json:"dtr,omitempty"`
}

type StatusArgs struct{}

type StatusResult struct {
	Connections []serial.ConnInfo `json:"connections"`
}

type CloseArgs struct {
	ConnectionID string `json:"connection_id"`
}

type CloseResult struct {
	ConnectionID string `json:"connection_id"`
	Closed       bool   `json:"closed"`
}
