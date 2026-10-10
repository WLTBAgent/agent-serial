package server

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wltbagent/agent-serial/internal/serial"
)

// toolServer binds the MCP tool handlers to a Registry.
type toolServer struct {
	reg *serial.Registry
}

// New builds the MCP server with all serial tools registered.
func New(reg *serial.Registry, version string) *mcp.Server {
	ts := &toolServer{reg: reg}

	srv := mcp.NewServer(
		&mcp.Implementation{Name: "agent-serial", Version: version},
		&mcp.ServerOptions{
			Instructions: "Serial console access for embedded devices. " +
				"Typical workflow: list_ports to discover devices, open with the device's baud rate " +
				"(115200 is the usual console default), write commands (append the console line ending " +
				"such as \"\\n\" or \"\\r\\n\" yourself; nothing is added automatically), then read the " +
				"response. The server buffers device output continuously, so read without duration_ms " +
				"returns everything accumulated since the last read; read with duration_ms waits for " +
				"output and stops early when the line goes idle (idle_timeout_ms). Always report " +
				"completion_reason. Use expect to react to device output — e.g. abort a bootloader's " +
				"autoboot countdown with pattern \"autoboot\" and send \"03\" (hex Ctrl-C) within its " +
				"short window; max_hits caps how many times the send repeats. Use status to list open " +
				"connections and buffered byte counts, and close connections when done.",
		},
	)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_ports",
		Description: "List serial ports on this system, with USB identity where available.",
	}, ts.listPorts)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "open",
		Description: "Open a device connection and start buffering its output. Transports: serial (default; port is a device path, baud_rate etc. apply), telnet (port is host:port), ssh (port is host:port, username plus key_path or password).",
	}, ts.open)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "write",
		Description: "Write data to an open connection. Line endings are not added automatically.",
	}, ts.write)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read",
		Description: "Read device output. Without duration_ms: drain buffered output immediately. " +
			"With duration_ms: open a capture window that stops early on idle line or max_bytes.",
	}, ts.read)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "expect",
		Description: "Wait for a substring in device output and send a payload when it appears, at most max_hits times. " +
			"Bootloader abort example: pattern \"autoboot\", send \"03\" (hex Ctrl-C), max_hits 1. " +
			"Returns all output collected during the wait.",
	}, ts.expect)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "set_control_lines",
		Description: "Set RTS and/or DTR. Toggling DTR resets many boards (Arduino, ESP32 dev boards).",
	}, ts.setControlLines)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "status",
		Description: "List open connections with their configuration, byte counters and buffered output.",
	}, ts.status)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "close",
		Description: "Close a connection and release the port.",
	}, ts.close)

	return srv
}

func (ts *toolServer) listPorts(ctx context.Context, req *mcp.CallToolRequest, args ListPortsArgs) (*mcp.CallToolResult, ListPortsResult, error) {
	ports, err := serial.ListPorts()
	if err != nil {
		return nil, ListPortsResult{}, err
	}
	if ports == nil {
		ports = []serial.PortInfo{}
	}
	return nil, ListPortsResult{Ports: ports}, nil
}

func (ts *toolServer) open(ctx context.Context, req *mcp.CallToolRequest, args OpenArgs) (*mcp.CallToolResult, OpenResult, error) {
	transport, err := serial.ParseTransport(args.Transport)
	if err != nil {
		return nil, OpenResult{}, err
	}
	target := serial.Target{
		Transport: transport,
		Port:      args.Port,
		Serial: serial.SerialConfig{
			Port:        args.Port,
			BaudRate:    args.BaudRate,
			DataBits:    args.DataBits,
			StopBits:    args.StopBits,
			Parity:      args.Parity,
			FlowControl: args.FlowControl,
		},
		Username:   args.Username,
		Password:   args.Password,
		KeyPath:    args.KeyPath,
		Passphrase: args.Passphrase,
		Command:    args.Command,
	}
	// Normalize the handler's copy first (default-fill, host:port completion)
	// so both the open and the summary below see canonical values.
	if err := target.Normalize(); err != nil {
		return nil, OpenResult{}, err
	}
	conn, err := ts.reg.Open(target)
	if err != nil {
		return nil, OpenResult{}, err
	}
	return nil, OpenResult{
		ConnectionID: conn.ID(),
		Transport:    string(target.Transport),
		Target:       target.Port,
		Config:       target.Display(),
	}, nil
}

func (ts *toolServer) write(ctx context.Context, req *mcp.CallToolRequest, args WriteArgs) (*mcp.CallToolResult, WriteResult, error) {
	conn, err := ts.reg.Get(args.ConnectionID)
	if err != nil {
		return nil, WriteResult{}, err
	}
	encoding, err := serial.NormalizeEncoding(args.Encoding)
	if err != nil {
		return nil, WriteResult{}, err
	}
	payload, err := serial.DecodeData(args.Data, encoding)
	if err != nil {
		return nil, WriteResult{}, err
	}
	n, err := conn.Write(payload)
	if err != nil {
		return nil, WriteResult{}, err
	}
	return nil, WriteResult{ConnectionID: conn.ID(), BytesWritten: n}, nil
}

func (ts *toolServer) read(ctx context.Context, req *mcp.CallToolRequest, args ReadArgs) (*mcp.CallToolResult, ReadResult, error) {
	conn, err := ts.reg.Get(args.ConnectionID)
	if err != nil {
		return nil, ReadResult{}, err
	}
	encoding, err := serial.NormalizeEncoding(args.Encoding)
	if err != nil {
		return nil, ReadResult{}, err
	}

	var (
		data   []byte
		result *serial.CaptureResult
	)

	if args.Peek {
		if args.DurationMs > 0 {
			return nil, ReadResult{}, fmt.Errorf("peek is only supported for immediate reads (duration_ms must be 0)")
		}
		data, err = conn.Peek(args.MaxBytes)
		if err != nil {
			return nil, ReadResult{}, err
		}
		result = &serial.CaptureResult{Data: data, Reason: serial.Drained}
	} else {
		opts := serial.CaptureOptions{
			MaxBytes:       args.MaxBytes,
			Duration:       time.Duration(args.DurationMs) * time.Millisecond,
			InitialTimeout: time.Duration(args.InitialTimeoutMs) * time.Millisecond,
			IdleTimeout:    time.Duration(args.IdleTimeoutMs) * time.Millisecond,
		}
		result, err = conn.Read(ctx, opts)
		if err != nil {
			return nil, ReadResult{}, err
		}
		data = result.Data
	}

	encoded, err := serial.EncodeData(data, encoding)
	if err != nil {
		return nil, ReadResult{}, err
	}

	out := ReadResult{
		ConnectionID:     conn.ID(),
		Data:             encoded,
		BytesRead:        len(data),
		Encoding:         encoding,
		CompletionReason: string(result.Reason),
		DroppedBytes:     conn.Ring().Dropped(),
	}
	if args.DurationMs > 0 {
		out.WaitedMs = result.Waited.Milliseconds()
		out.ElapsedMs = result.Elapsed.Milliseconds()
		for _, ch := range result.Chunks {
			out.Chunks = append(out.Chunks, ReadChunk{Offset: ch.Offset, Bytes: len(ch.Data)})
		}
	}
	return nil, out, nil
}

func (ts *toolServer) expect(ctx context.Context, req *mcp.CallToolRequest, args ExpectArgs) (*mcp.CallToolResult, ExpectResult, error) {
	conn, err := ts.reg.Get(args.ConnectionID)
	if err != nil {
		return nil, ExpectResult{}, err
	}
	encoding, err := serial.NormalizeEncoding(args.Encoding)
	if err != nil {
		return nil, ExpectResult{}, err
	}
	sendEncoding, err := serial.NormalizeEncoding(args.SendEncoding)
	if err != nil {
		return nil, ExpectResult{}, err
	}
	var send []byte
	if args.Send != "" {
		send, err = serial.DecodeData(args.Send, sendEncoding)
		if err != nil {
			return nil, ExpectResult{}, fmt.Errorf("send: %w", err)
		}
	}

	res, err := conn.Expect(ctx, serial.ExpectOptions{
		Pattern:        []byte(args.Pattern),
		Send:           send,
		SendDelay:      time.Duration(args.SendDelayMs) * time.Millisecond,
		MaxHits:        args.MaxHits,
		MaxBytes:       args.MaxBytes,
		Duration:       time.Duration(args.DurationMs) * time.Millisecond,
		InitialTimeout: time.Duration(args.InitialTimeoutMs) * time.Millisecond,
		IdleTimeout:    time.Duration(args.IdleTimeoutMs) * time.Millisecond,
	})
	if err != nil {
		return nil, ExpectResult{}, err
	}

	encoded, err := serial.EncodeData(res.Data, encoding)
	if err != nil {
		return nil, ExpectResult{}, err
	}

	return nil, ExpectResult{
		ConnectionID:     conn.ID(),
		Data:             encoded,
		BytesRead:        len(res.Data),
		Encoding:         encoding,
		CompletionReason: string(res.Reason),
		Hits:             res.Hits,
		Sent:             res.Sent,
		MatchOffsets:     res.MatchOffsets,
		WaitedMs:         res.Waited.Milliseconds(),
		ElapsedMs:        res.Elapsed.Milliseconds(),
		DroppedBytes:     conn.Ring().Dropped(),
	}, nil
}

func (ts *toolServer) setControlLines(ctx context.Context, req *mcp.CallToolRequest, args ControlLinesArgs) (*mcp.CallToolResult, ControlLinesResult, error) {
	conn, err := ts.reg.Get(args.ConnectionID)
	if err != nil {
		return nil, ControlLinesResult{}, err
	}
	if args.RTS == nil && args.DTR == nil {
		return nil, ControlLinesResult{}, fmt.Errorf("at least one of rts or dtr must be set")
	}
	if args.RTS != nil {
		if err := conn.SetRTS(*args.RTS); err != nil {
			return nil, ControlLinesResult{}, fmt.Errorf("set RTS: %w", err)
		}
	}
	if args.DTR != nil {
		if err := conn.SetDTR(*args.DTR); err != nil {
			return nil, ControlLinesResult{}, fmt.Errorf("set DTR: %w", err)
		}
	}
	return nil, ControlLinesResult{ConnectionID: conn.ID(), RTS: args.RTS, DTR: args.DTR}, nil
}

func (ts *toolServer) status(ctx context.Context, req *mcp.CallToolRequest, args StatusArgs) (*mcp.CallToolResult, StatusResult, error) {
	conns := ts.reg.List()
	if conns == nil {
		conns = []serial.ConnInfo{}
	}
	return nil, StatusResult{Connections: conns}, nil
}

func (ts *toolServer) close(ctx context.Context, req *mcp.CallToolRequest, args CloseArgs) (*mcp.CallToolResult, CloseResult, error) {
	if err := ts.reg.Close(args.ConnectionID); err != nil {
		return nil, CloseResult{}, err
	}
	return nil, CloseResult{ConnectionID: args.ConnectionID, Closed: true}, nil
}
