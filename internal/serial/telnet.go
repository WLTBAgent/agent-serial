package serial

import (
	"fmt"
	"net"
	"time"
)

// Telnet protocol bytes (RFC 854).
const (
	iac      = 255 // Interpret As Command
	dontByte = 254
	doByte   = 253
	wontByte = 252
	willByte = 251
	sbByte   = 250 // subnegotiation begin
	gaByte   = 249 // go ahead
	elByte   = 248 // erase line
	ecByte   = 247 // erase character
	aytByte  = 246 // are you there
	aoByte   = 245 // abort output
	ipByte   = 244 // interrupt process
	brkByte  = 243 // break
	nop      = 241 // no operation
	seByte   = 240 // subnegotiation end
)

const dialTimeout = 10 * time.Second

// openTelnetStream dials a telnet target and returns a ByteStream that
// hides protocol negotiation: outgoing IAC sequences from the server are
// parsed and refused (WILL→DONT, DO→WONT), and data we send has 0xFF
// escaped, so callers see a clean byte stream.
func openTelnetStream(target Target) (ByteStream, error) {
	conn, err := net.DialTimeout("tcp", target.Port, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("telnet dial %s: %w", target.Port, err)
	}
	return newTelnetStream(conn), nil
}

type telnetStream struct {
	conn net.Conn

	pending []byte // decoded application bytes not yet returned by Read
	raw     []byte // unparsed bytes carried over from the last Read
	replies []byte // IAC responses queued for the server
}

func newTelnetStream(conn net.Conn) *telnetStream {
	return &telnetStream{conn: conn}
}

func (t *telnetStream) Read(p []byte) (int, error) {
	for {
		if len(t.replies) > 0 {
			// Flush negotiation refusals before blocking on the socket so a
			// server waiting for our answer never stalls behind data.
			if _, err := t.conn.Write(t.replies); err != nil {
				return 0, err
			}
			t.replies = t.replies[:0]
		}
		if len(t.pending) > 0 {
			n := copy(p, t.pending)
			t.pending = t.pending[n:]
			return n, nil
		}
		buf := make([]byte, 4096)
		n, err := t.conn.Read(buf)
		if n > 0 {
			t.parse(buf[:n])
		}
		if err != nil {
			return 0, err
		}
	}
}

// parse decodes raw server bytes into t.pending, collecting negotiation
// replies. Any trailing partial sequence is kept in t.raw for the next
// Read, so commands split across TCP segments are handled.
func (t *telnetStream) parse(data []byte) {
	buf := append(t.raw, data...)
	t.raw = nil

	for i := 0; i < len(buf); {
		b := buf[i]
		if b != iac {
			// Copy the run of plain bytes up to the next IAC.
			j := i
			for j < len(buf) && buf[j] != iac {
				j++
			}
			t.pending = append(t.pending, buf[i:j]...)
			i = j
			continue
		}

		// Need at least IAC + command.
		if i+1 >= len(buf) {
			t.raw = append(t.raw, buf[i:]...)
			return
		}
		switch buf[i+1] {
		case iac: // escaped 0xFF literal
			t.pending = append(t.pending, iac)
			i += 2
		case willByte, wontByte, doByte, dontByte:
			// Need IAC + command + option.
			if i+2 >= len(buf) {
				t.raw = append(t.raw, buf[i:]...)
				return
			}
			cmd, opt := buf[i+1], buf[i+2]
			switch cmd {
			case willByte:
				// We support no options: refuse everything.
				t.replies = append(t.replies, iac, dontByte, opt)
			case doByte:
				t.replies = append(t.replies, iac, wontByte, opt)
			}
			i += 3
		case sbByte: // subnegotiation: skip through IAC SE
			end, ok := findSubnegEnd(buf, i+2)
			if !ok {
				t.raw = append(t.raw, buf[i:]...)
				return
			}
			i = end
		case seByte, nop, brkByte, ipByte, aoByte, aytByte, ecByte, elByte, gaByte:
			// Standalone commands: consume and ignore.
			i += 2
		default: // unknown command byte: skip it
			i += 2
		}
	}
}

func findSubnegEnd(buf []byte, from int) (int, bool) {
	for i := from; i < len(buf); {
		if buf[i] != iac {
			i++
			continue
		}
		if i+1 >= len(buf) {
			return 0, false
		}
		switch buf[i+1] {
		case seByte:
			return i + 2, true
		case iac: // escaped 0xFF inside subnegotiation payload
			i += 2
		default:
			return 0, false // malformed: bail out, keep everything
		}
	}
	return 0, false
}

func (t *telnetStream) Write(p []byte) (int, error) {
	if !hasFF(p) {
		return t.conn.Write(p)
	}
	escaped := make([]byte, 0, len(p)+8)
	for _, b := range p {
		if b == iac {
			escaped = append(escaped, iac, iac)
		} else {
			escaped = append(escaped, b)
		}
	}
	n, err := t.conn.Write(escaped)
	if n > len(p) {
		n = len(p)
	}
	return n, err
}

func (t *telnetStream) Close() error { return t.conn.Close() }

func hasFF(p []byte) bool {
	for _, b := range p {
		if b == iac {
			return true
		}
	}
	return false
}
