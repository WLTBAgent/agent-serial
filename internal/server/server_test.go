package server

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wltbagent/agent-serial/internal/serial"
)

// fakeOpener mirrors the serial-test fake; duplicated here to keep the
// server test self-contained.
type fakePort struct {
	ch      chan byte
	written chan []byte
	closed  chan struct{}
}

func newFakePort() *fakePort {
	return &fakePort{ch: make(chan byte, 4096), written: make(chan []byte, 16), closed: make(chan struct{})}
}

func (f *fakePort) Read(p []byte) (int, error) {
	select {
	case b := <-f.ch:
		p[0] = b
		n := 1
		for n < len(p) {
			select {
			case b := <-f.ch:
				p[n] = b
				n++
			default:
				return n, nil
			}
		}
		return n, nil
	case <-f.closed:
		return 0, errPortClosed
	}
}

func (f *fakePort) Write(p []byte) (int, error) {
	select {
	case f.written <- append([]byte(nil), p...):
		return len(p), nil
	case <-f.closed:
		return 0, errPortClosed
	}
}

func (f *fakePort) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}
func (f *fakePort) SetRTS(bool) error { return nil }
func (f *fakePort) SetDTR(bool) error { return nil }

func (f *fakePort) Feed(data []byte) {
	for _, b := range data {
		f.ch <- b
	}
}

type fakeOpener struct {
	ports map[string]*fakePort
}

func (o *fakeOpener) open(t serial.Target) (serial.ByteStream, error) {
	f := newFakePort()
	o.ports[t.Port] = f
	return f, nil
}

var errPortClosed = errorString("port closed")

type errorString string

func (e errorString) Error() string { return string(e) }

// startServer wires a registry over fake ports to an in-memory MCP session
// and returns the client session plus the opener.
func startServer(t *testing.T) (*mcp.ClientSession, *fakeOpener) {
	t.Helper()

	opener := &fakeOpener{ports: map[string]*fakePort{}}
	reg := serial.NewRegistryWithOpener(serial.RegistryOptions{RingCapacity: 4096}, opener.open)
	srv := New(reg, "test")

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, opener
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned tool error: %v", name, res.Content[0])
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal structured output: %v", name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: unmarshal structured output: %v", name, err)
	}
	return out
}

func TestToolsListed(t *testing.T) {
	cs, _ := startServer(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"list_ports": false, "open": false, "write": false, "read": false,
		"set_control_lines": false, "status": false, "close": false,
	}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q not listed", name)
		}
	}
}

func TestOpenWriteReadRoundtrip(t *testing.T) {
	cs, opener := startServer(t)

	open := callTool(t, cs, "open", map[string]any{"port": "/dev/ttyFAKE0", "baud_rate": 115200})
	connID, _ := open["connection_id"].(string)
	if connID == "" {
		t.Fatalf("no connection_id in %v", open)
	}

	write := callTool(t, cs, "write", map[string]any{"connection_id": connID, "data": "help\n"})
	if write["bytes_written"].(float64) != 5 {
		t.Fatalf("bytes_written = %v", write["bytes_written"])
	}

	// Device responds; a windowed read waits for the response instead of
	// racing the background reader.
	opener.ports["/dev/ttyFAKE0"].Feed([]byte("usage: ...\n"))

	read := callTool(t, cs, "read", map[string]any{
		"connection_id":   connID,
		"duration_ms":     1000,
		"idle_timeout_ms": 100,
	})
	if read["completion_reason"] != "idle_timeout" {
		t.Fatalf("completion_reason = %v", read["completion_reason"])
	}
	if read["data"] != "usage: ...\n" {
		t.Fatalf("data = %q", read["data"])
	}
	if read["bytes_read"].(float64) != 11 {
		t.Fatalf("bytes_read = %v", read["bytes_read"])
	}
}

func TestReadCaptureWindow(t *testing.T) {
	cs, opener := startServer(t)
	open := callTool(t, cs, "open", map[string]any{"port": "/dev/ttyFAKE0", "baud_rate": 9600})
	connID := open["connection_id"].(string)

	go func() {
		time.Sleep(30 * time.Millisecond)
		opener.ports["/dev/ttyFAKE0"].Feed([]byte("boot ok\n"))
	}()

	read := callTool(t, cs, "read", map[string]any{
		"connection_id":   connID,
		"duration_ms":     500,
		"idle_timeout_ms": 60,
	})
	if read["completion_reason"] != "idle_timeout" {
		t.Fatalf("completion_reason = %v, want idle_timeout", read["completion_reason"])
	}
	if read["data"] != "boot ok\n" {
		t.Fatalf("data = %q", read["data"])
	}
}

func TestExpectToolBootAbort(t *testing.T) {
	cs, opener := startServer(t)
	open := callTool(t, cs, "open", map[string]any{"port": "/dev/ttyFAKE0", "baud_rate": 115200})
	connID := open["connection_id"].(string)

	// Device starts its countdown after the connection opens.
	go func() {
		time.Sleep(30 * time.Millisecond)
		opener.ports["/dev/ttyFAKE0"].Feed([]byte("\nHit any key to stop autoboot: 3 "))
	}()

	res := callTool(t, cs, "expect", map[string]any{
		"connection_id":   connID,
		"pattern":         "autoboot",
		"send":            "03",
		"send_encoding":   "hex",
		"duration_ms":     2000,
		"idle_timeout_ms": 500,
	})
	if res["completion_reason"] != "matched" {
		t.Fatalf("completion_reason = %v, want matched", res["completion_reason"])
	}
	if res["hits"].(float64) != 1 {
		t.Fatalf("hits = %v", res["hits"])
	}
	if res["sent_bytes"].(float64) != 1 {
		t.Fatalf("sent_bytes = %v", res["sent_bytes"])
	}
	// Everything up to and including the match is guaranteed in Data; bytes
	// after the match may still be in the ring when expect returns.
	if data := res["data"].(string); !strings.Contains(data, "stop autoboot") {
		t.Fatalf("data = %q", data)
	}

	// The Ctrl-C must have reached the port.
	select {
	case w := <-opener.ports["/dev/ttyFAKE0"].written:
		if len(w) != 1 || w[0] != 0x03 {
			t.Fatalf("port written %v, want 0x03", w)
		}
	default:
		t.Fatal("nothing written to port; Ctrl-C was not sent")
	}
}

func TestStatusAndClose(t *testing.T) {
	cs, _ := startServer(t)

	status := callTool(t, cs, "status", map[string]any{})
	if len(status["connections"].([]any)) != 0 {
		t.Fatal("expected no connections")
	}

	open := callTool(t, cs, "open", map[string]any{"port": "/dev/ttyFAKE1", "baud_rate": 115200})
	connID := open["connection_id"].(string)

	status = callTool(t, cs, "status", map[string]any{})
	conns := status["connections"].([]any)
	if len(conns) != 1 {
		t.Fatalf("got %d connections, want 1", len(conns))
	}
	c0 := conns[0].(map[string]any)
	if c0["transport"] != "serial" || c0["target"] != "/dev/ttyFAKE1" {
		t.Fatalf("status entry = %v", c0)
	}
	ser := c0["serial"].(map[string]any)
	if ser["baud_rate"].(float64) != 115200 {
		t.Fatalf("status serial = %v", ser)
	}

	callTool(t, cs, "close", map[string]any{"connection_id": connID})
	status = callTool(t, cs, "status", map[string]any{})
	if len(status["connections"].([]any)) != 0 {
		t.Fatal("connection not removed on close")
	}
}

// TestOpenTelnetEndToEnd drives a real telnet connection (in-process TCP
// server) through the MCP tool surface.
func TestOpenTelnetEndToEnd(t *testing.T) {
	// Console server: refuses nothing, sends a banner then echoes.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte{0xff, 0xfb, 0x01}) // IAC WILL ECHO
		conn.Write([]byte("console ready\n"))
		buf := make([]byte, 256)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				conn.Write(buf[:n]) // echo
			}
			if err != nil {
				return
			}
		}
	}()

	// Real registry (real dialer), wired to the MCP server under test.
	reg := serial.NewRegistry(serial.RegistryOptions{RingCapacity: 4096})
	srv := New(reg, "test")
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	open := callTool(t, cs, "open", map[string]any{
		"transport": "telnet",
		"port":      listener.Addr().String(),
	})
	connID := open["connection_id"].(string)
	if open["transport"] != "telnet" {
		t.Fatalf("open result = %v", open)
	}

	read := callTool(t, cs, "read", map[string]any{
		"connection_id":   connID,
		"duration_ms":     1000,
		"idle_timeout_ms": 300,
	})
	if data := read["data"].(string); !strings.Contains(data, "console ready") {
		t.Fatalf("banner = %q", data)
	}

	callTool(t, cs, "write", map[string]any{"connection_id": connID, "data": "hi\n"})
	read = callTool(t, cs, "read", map[string]any{
		"connection_id":   connID,
		"duration_ms":     1000,
		"idle_timeout_ms": 300,
	})
	if data := read["data"].(string); !strings.Contains(data, "hi") {
		t.Fatalf("echo = %q", read["data"])
	}

	// Control lines are serial-only and must say so cleanly.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "set_control_lines",
		Arguments: map[string]any{"connection_id": connID, "dtr": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("set_control_lines on telnet should be refused")
	}

	callTool(t, cs, "close", map[string]any{"connection_id": connID})
}

func TestToolErrorsAreReported(t *testing.T) {
	cs, _ := startServer(t)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "read",
		Arguments: map[string]any{"connection_id": "nope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for unknown connection")
	}

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "open",
		Arguments: map[string]any{
			"port": "/dev/ttyFAKE2", "baud_rate": 115200, "flow_control": "hardware",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for unsupported flow control")
	}
}
