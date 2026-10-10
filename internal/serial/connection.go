package serial

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	bugserial "go.bug.st/serial"
)

// ByteStream is a bidirectional byte transport: a serial port, a telnet
// connection or an SSH session all satisfy it.
type ByteStream interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

// ControlLines is optionally implemented by transports that have modem
// control lines. Only serial transports do.
type ControlLines interface {
	SetRTS(bool) error
	SetDTR(bool) error
}

var ErrConnectionClosed = errors.New("connection closed")

// Connection owns one open device connection. A background goroutine reads
// the stream continuously into a ring buffer, so device output that arrives
// between tool calls is retained. Reads consume from the ring; writes go
// straight to the stream and never block on reads.
type Connection struct {
	id        string
	transport Transport
	target    string       // device path or host:port
	serial    SerialConfig // zero for network transports
	stream    ByteStream
	ring      *Ring
	createdAt time.Time

	stopCh chan struct{}
	doneCh chan struct{}

	writeMu   sync.Mutex // serializes writes to the stream
	captureMu sync.Mutex // one reader at a time, or reads would split data
	closeOnce sync.Once
	closeErr  error

	bytesSent atomic.Uint64
	bytesRecv atomic.Uint64

	errMu   sync.RWMutex
	readErr error
	closing bool
}

// NewConnectionWithStream wraps an already-open ByteStream. It is the
// injection point used by transports and tests.
func NewConnectionWithStream(transport Transport, target string, sconf SerialConfig, ringCapacity int, stream ByteStream) *Connection {
	c := &Connection{
		id:        newID(),
		transport: transport,
		target:    target,
		serial:    sconf,
		stream:    stream,
		ring:      NewRing(ringCapacity),
		createdAt: time.Now(),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Connection) readLoop() {
	defer close(c.doneCh)
	buf := make([]byte, 4096)
	for {
		select {
		case <-c.stopCh:
			return
		default:
		}
		n, err := c.stream.Read(buf)
		if n > 0 {
			c.bytesRecv.Add(uint64(n))
			c.ring.Write(buf[:n])
		}
		if err != nil {
			c.errMu.Lock()
			c.readErr = err
			c.errMu.Unlock()
			return
		}
	}
}

func (c *Connection) ID() string           { return c.id }
func (c *Connection) Ring() *Ring          { return c.ring }
func (c *Connection) CreatedAt() time.Time { return c.createdAt }
func (c *Connection) Transport() Transport { return c.transport }

func (c *Connection) checkOpen() error {
	c.errMu.RLock()
	defer c.errMu.RUnlock()
	if c.closing || c.readErr != nil {
		if c.readErr != nil {
			return fmt.Errorf("connection lost on %s: %w", c.target, c.readErr)
		}
		return ErrConnectionClosed
	}
	return nil
}

// Write sends p to the device. It is safe to call while a capture is running.
func (c *Connection) Write(p []byte) (int, error) {
	if err := c.checkOpen(); err != nil {
		return 0, err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	n, err := c.stream.Write(p)
	c.bytesSent.Add(uint64(n))
	return n, err
}

// Read collects bytes per opts (see CaptureOptions). Only one read may run
// at a time on a connection — a second concurrent read is refused
// immediately rather than queued or allowed to split the data.
func (c *Connection) Read(ctx context.Context, opts CaptureOptions) (*CaptureResult, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	if !c.captureMu.TryLock() {
		return nil, fmt.Errorf("another read is already in progress on %s; wait for it to finish", c.target)
	}
	defer c.captureMu.Unlock()
	return c.ring.Capture(ctx, opts)
}

// Peek returns buffered bytes without consuming them. Only supported for
// immediate (no-window) reads.
func (c *Connection) Peek(maxBytes int) ([]byte, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	c.captureMu.Lock()
	defer c.captureMu.Unlock()
	buf := make([]byte, maxBytes)
	n := c.ring.Peek(buf)
	return buf[:n], nil
}

// SetRTS sets the RTS line. Serial transports only.
func (c *Connection) SetRTS(level bool) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	cl, ok := c.stream.(ControlLines)
	if !ok {
		return fmt.Errorf("control lines are not available on %s connections", c.transport)
	}
	return cl.SetRTS(level)
}

// SetDTR sets the DTR line. Serial transports only.
func (c *Connection) SetDTR(level bool) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	cl, ok := c.stream.(ControlLines)
	if !ok {
		return fmt.Errorf("control lines are not available on %s connections", c.transport)
	}
	return cl.SetDTR(level)
}

// Close stops the reader and releases the stream. It is idempotent; the
// second call reports nil.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		c.errMu.Lock()
		c.closing = true
		c.errMu.Unlock()
		close(c.stopCh)
		c.closeErr = c.stream.Close()
		// A closed unix serial port unblocks pending reads via a signal
		// pipe, so doneCh closes promptly; bound the wait anyway.
		select {
		case <-c.doneCh:
		case <-time.After(2 * time.Second):
		}
	})
	return c.closeErr
}

// ConnInfo is a status snapshot for one connection.
type ConnInfo struct {
	ID          string      `json:"id"`
	Transport   Transport   `json:"transport"`
	Target      string      `json:"target"`
	Serial      *SerialInfo `json:"serial,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	BytesSent   uint64      `json:"bytes_sent"`
	BytesRecv   uint64      `json:"bytes_received"`
	RingUsed    int         `json:"ring_buffered_bytes"`
	RingDropped uint64      `json:"ring_dropped_bytes"`
	Error       string      `json:"error,omitempty"`
}

// SerialInfo carries the line settings, for serial connections only.
type SerialInfo struct {
	BaudRate    int    `json:"baud_rate"`
	DataBits    int    `json:"data_bits"`
	StopBits    int    `json:"stop_bits"`
	Parity      string `json:"parity"`
	FlowControl string `json:"flow_control"`
}

func (c *Connection) Info() ConnInfo {
	c.errMu.RLock()
	defer c.errMu.RUnlock()
	info := ConnInfo{
		ID:          c.id,
		Transport:   c.transport,
		Target:      c.target,
		CreatedAt:   c.createdAt,
		BytesSent:   c.bytesSent.Load(),
		BytesRecv:   c.bytesRecv.Load(),
		RingUsed:    c.ring.Len(),
		RingDropped: c.ring.Dropped(),
	}
	if c.transport == TransportSerial {
		info.Serial = &SerialInfo{
			BaudRate:    c.serial.BaudRate,
			DataBits:    c.serial.DataBits,
			StopBits:    c.serial.StopBits,
			Parity:      c.serial.Parity,
			FlowControl: c.serial.FlowControl,
		}
	}
	if c.readErr != nil {
		info.Error = c.readErr.Error()
	}
	return info
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("agent-serial: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// openSerialStream opens a local serial port via go.bug.st/serial.
func openSerialStream(sconf SerialConfig) (ByteStream, error) {
	mode := &bugserial.Mode{
		BaudRate: sconf.BaudRate,
		DataBits: sconf.DataBits,
	}
	switch sconf.Parity {
	case "odd":
		mode.Parity = bugserial.OddParity
	case "even":
		mode.Parity = bugserial.EvenParity
	default:
		mode.Parity = bugserial.NoParity
	}
	if sconf.StopBits == 2 {
		mode.StopBits = bugserial.TwoStopBits
	} else {
		mode.StopBits = bugserial.OneStopBit
	}
	raw, err := bugserial.Open(sconf.Port, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", sconf.Port, err)
	}
	return raw, nil
}
