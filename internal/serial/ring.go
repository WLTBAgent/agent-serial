package serial

import "sync"

// Ring is a fixed-capacity, drop-oldest byte ring buffer with a level
// notification for waiters. It lets a background reader goroutine accumulate
// device output continuously, so data arriving between tool calls is retained
// instead of being lost to the OS driver buffer.
type Ring struct {
	mu      sync.Mutex
	buf     []byte
	head    int // index of oldest byte
	count   int
	dropped uint64

	// notify receives a token whenever data is written, so a waiter that has
	// already observed an empty ring is guaranteed a wakeup for any append
	// after that observation. Spurious wakeups are allowed.
	notify chan struct{}
}

func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1
	}
	return &Ring{
		buf:    make([]byte, capacity),
		notify: make(chan struct{}, 1),
	}
}

// Write appends p, dropping the oldest bytes when full. It returns the number
// of bytes of p that were retained (which may be less than len(p) when p alone
// exceeds capacity).
func (r *Ring) Write(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(p) >= len(r.buf) {
		// Keep only the tail that fits; everything else (including any bytes
		// already buffered) is dropped.
		r.dropped += uint64(r.count + len(p) - len(r.buf))
		copy(r.buf, p[len(p)-len(r.buf):])
		r.head = 0
		r.count = len(r.buf)
		r.signalLocked()
		return len(p)
	}

	free := len(r.buf) - r.count
	if len(p) > free {
		overflow := len(p) - free
		r.dropped += uint64(overflow)
		r.head = (r.head + overflow) % len(r.buf)
		r.count -= overflow
	}
	tail := (r.head + r.count) % len(r.buf)
	n := copy(r.buf[tail:], p)
	if n < len(p) {
		n += copy(r.buf, p[n:])
	}
	r.count += len(p)
	r.signalLocked()
	return len(p)
}

func (r *Ring) signalLocked() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// Read drains up to len(p) bytes, oldest first, consuming them.
func (r *Ring) Read(p []byte) int {
	n, _ := r.read(p, true)
	return n
}

// Peek copies up to len(p) bytes, oldest first, without consuming them.
func (r *Ring) Peek(p []byte) int {
	n, _ := r.read(p, false)
	return n
}

func (r *Ring) read(p []byte, consume bool) (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := min(len(p), r.count)
	for i := range n {
		p[i] = r.buf[(r.head+i)%len(r.buf)]
	}
	if consume {
		r.head = (r.head + n) % len(r.buf)
		r.count -= n
	}
	return n, 0
}

func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *Ring) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// WaitCh returns a channel that receives a token whenever data is appended.
// Callers must re-check Len/Read after waking: the token is level-triggered.
func (r *Ring) WaitCh() <-chan struct{} { return r.notify }
