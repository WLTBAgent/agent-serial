package serial

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RegistryOptions configures a Registry.
type RegistryOptions struct {
	MaxConnections int      // 0 → 16
	RingCapacity   int      // 0 → 1 MiB
	AllowedPorts   []string // serial device paths; substring match; empty = all
	BlockedPorts   []string // serial device paths; takes precedence over allowed
	AllowedHosts   []string // telnet/ssh host:port; substring match; empty = all
	BlockedHosts   []string // telnet/ssh host:port; takes precedence over allowed

	SSHKnownHostsFile string // 0 → <user config dir>/agent-serial/known_hosts
}

const (
	DefaultMaxConnections = 16
	DefaultRingCapacity   = 1 << 20
)

// Opener creates a ByteStream for a validated target. The default uses the
// OS serial stack or dials the network; tests inject fakes.
type Opener func(t Target) (ByteStream, error)

// Registry tracks open connections and enforces per-process policy:
// connection limits, port/host allow-block lists, and one connection per
// target.
type Registry struct {
	mu       sync.Mutex
	conns    map[string]*Connection
	byTarget map[string]string

	opts   RegistryOptions
	opener Opener
}

func NewRegistry(opts RegistryOptions) *Registry {
	if opts.MaxConnections <= 0 {
		opts.MaxConnections = DefaultMaxConnections
	}
	if opts.RingCapacity <= 0 {
		opts.RingCapacity = DefaultRingCapacity
	}
	if opts.SSHKnownHostsFile == "" {
		opts.SSHKnownHostsFile = defaultKnownHostsFile()
	}
	r := &Registry{
		conns:    map[string]*Connection{},
		byTarget: map[string]string{},
		opts:     opts,
	}
	r.opener = func(t Target) (ByteStream, error) {
		if t.Transport == TransportSSH {
			return openSSHStream(t, opts.SSHKnownHostsFile)
		}
		return openStream(t)
	}
	return r
}

func NewRegistryWithOpener(opts RegistryOptions, opener Opener) *Registry {
	r := NewRegistry(opts)
	r.opener = opener
	return r
}

func defaultKnownHostsFile() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "agent-serial", "known_hosts")
	}
	return filepath.Join(".agent-serial", "known_hosts")
}

// openStream dispatches a validated target to its transport.
func openStream(t Target) (ByteStream, error) {
	switch t.Transport {
	case TransportSerial:
		return openSerialStream(t.Serial)
	case TransportTelnet:
		return openTelnetStream(t)
	default:
		return nil, fmt.Errorf("unsupported transport %q", t.Transport)
	}
}

// Open validates the target, applies policy, opens the stream and starts
// the background reader.
func (r *Registry) Open(t Target) (*Connection, error) {
	if t.Transport == "" {
		t.Transport = TransportSerial
	}
	if err := t.Normalize(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.checkPolicy(t); err != nil {
		return nil, err
	}
	if len(r.conns) >= r.opts.MaxConnections {
		return nil, fmt.Errorf("connection limit reached (%d); close a connection first", r.opts.MaxConnections)
	}
	key := string(t.Transport) + "|" + t.Port
	if id, ok := r.byTarget[key]; ok {
		return nil, fmt.Errorf("%s %s is already open as connection %s", t.Transport, t.Port, id)
	}

	stream, err := r.opener(t)
	if err != nil {
		return nil, err
	}
	conn := NewConnectionWithStream(t.Transport, t.Port, t.Serial, r.opts.RingCapacity, stream)
	r.conns[conn.id] = conn
	r.byTarget[key] = conn.id
	return conn, nil
}

func (r *Registry) checkPolicy(t Target) error {
	if t.Transport == TransportSerial {
		for _, blocked := range r.opts.BlockedPorts {
			if strings.Contains(t.Port, blocked) {
				return fmt.Errorf("port %s is blocked by server policy", t.Port)
			}
		}
		if len(r.opts.AllowedPorts) == 0 {
			return nil
		}
		for _, allowed := range r.opts.AllowedPorts {
			if strings.Contains(t.Port, allowed) {
				return nil
			}
		}
		return fmt.Errorf("port %s is not on the allowed list", t.Port)
	}

	// Network transports: without any allow list this server would accept
	// connections to arbitrary hosts.
	for _, blocked := range r.opts.BlockedHosts {
		if strings.Contains(t.Port, blocked) {
			return fmt.Errorf("host %s is blocked by server policy", t.Port)
		}
	}
	if len(r.opts.AllowedHosts) == 0 {
		return nil
	}
	for _, allowed := range r.opts.AllowedHosts {
		if strings.Contains(t.Port, allowed) {
			return nil
		}
	}
	return fmt.Errorf("host %s is not on the allowed list", t.Port)
}

// Get returns the connection with the given id.
func (r *Registry) Get(id string) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	conn, ok := r.conns[id]
	if !ok {
		return nil, fmt.Errorf("no such connection %q; use status to list open connections", id)
	}
	return conn, nil
}

// Close closes and removes the connection with the given id.
func (r *Registry) Close(id string) error {
	r.mu.Lock()
	conn, ok := r.conns[id]
	if ok {
		delete(r.conns, id)
		delete(r.byTarget, string(conn.transport)+"|"+conn.target)
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such connection %q", id)
	}
	return conn.Close()
}

// List returns status snapshots of all open connections.
func (r *Registry) List() []ConnInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ConnInfo, 0, len(r.conns))
	for _, conn := range r.conns {
		out = append(out, conn.Info())
	}
	return out
}

// CloseAll closes every connection; used on server shutdown.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	conns := make([]*Connection, 0, len(r.conns))
	for id, conn := range r.conns {
		conns = append(conns, conn)
		delete(r.conns, id)
		delete(r.byTarget, string(conn.transport)+"|"+conn.target)
	}
	r.mu.Unlock()
	for _, conn := range conns {
		conn.Close()
	}
}
