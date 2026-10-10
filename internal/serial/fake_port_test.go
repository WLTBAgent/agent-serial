package serial

import (
	"errors"
	"sync"
)

// fakePort is a RawPort for tests: feed bytes to make Read return them;
// written bytes are recorded for inspection.
type fakePort struct {
	mu      sync.Mutex
	cond    *sync.Cond
	queued  [][]byte
	written []byte
	closed  bool
	rts     bool
	dtr     bool
}

func newFakePort() *fakePort {
	f := &fakePort{}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *fakePort) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.queued) == 0 && !f.closed {
		f.cond.Wait()
	}
	if len(f.queued) == 0 {
		return 0, errors.New("fake port closed")
	}
	data := f.queued[0]
	n := copy(p, data)
	if n < len(data) {
		f.queued[0] = data[n:]
	} else {
		f.queued = f.queued[1:]
	}
	return n, nil
}

func (f *fakePort) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errors.New("fake port closed")
	}
	f.written = append(f.written, p...)
	return len(p), nil
}

func (f *fakePort) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	f.cond.Broadcast()
	return nil
}

func (f *fakePort) SetRTS(level bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rts = level
	return nil
}

func (f *fakePort) SetDTR(level bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dtr = level
	return nil
}

func (f *fakePort) Feed(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, append([]byte(nil), data...))
	f.cond.Broadcast()
}

func (f *fakePort) Written() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.written...)
}

func (f *fakePort) Lines() (rts, dtr bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rts, f.dtr
}

// fakeOpener tracks fake ports by path for a registry under test.
type fakeOpener struct {
	mu    sync.Mutex
	ports map[string]*fakePort
}

func newFakeOpener() *fakeOpener {
	return &fakeOpener{ports: map[string]*fakePort{}}
}

func (o *fakeOpener) open(t Target) (ByteStream, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := newFakePort()
	o.ports[t.Port] = f
	return f, nil
}

func (o *fakeOpener) get(path string) *fakePort {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ports[path]
}

// validTarget builds a canonical serial target for tests.
func validTarget(path string) Target {
	return Target{
		Transport: TransportSerial,
		Port:      path,
		Serial:    SerialConfig{Port: path, BaudRate: 115200},
	}
}
