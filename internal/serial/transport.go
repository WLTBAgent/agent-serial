package serial

import (
	"fmt"
	"net"
	"strings"
)

// Transport names a connection medium.
type Transport string

const (
	TransportSerial Transport = "serial"
	TransportTelnet Transport = "telnet"
	TransportSSH    Transport = "ssh"
)

// ParseTransport normalizes a transport name; "" means serial.
func ParseTransport(s string) (Transport, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(TransportSerial):
		return TransportSerial, nil
	case string(TransportTelnet):
		return TransportTelnet, nil
	case string(TransportSSH):
		return TransportSSH, nil
	default:
		return "", fmt.Errorf("unsupported transport %q (use serial, telnet or ssh)", s)
	}
}

// SerialConfig holds the serial-specific line settings. For network
// transports it is ignored and left zeroed.
type SerialConfig struct {
	Port        string `json:"port"`
	BaudRate    int    `json:"baud_rate"`
	DataBits    int    `json:"data_bits"`    // 0 → 8
	StopBits    int    `json:"stop_bits"`    // 0 → 1
	Parity      string `json:"parity"`       // ""|none|odd|even
	FlowControl string `json:"flow_control"` // ""|none (others rejected)
}

// Target describes what open should connect to.
type Target struct {
	Transport Transport    `json:"transport"`
	Port      string       `json:"port"` // device path (serial) or host:port (telnet/ssh)
	Serial    SerialConfig `json:"serial,omitempty"`

	// SSH-only fields.
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`   // used when KeyPath is empty
	KeyPath    string `json:"key_path,omitempty"`   // preferred over password
	Passphrase string `json:"passphrase,omitempty"` // for encrypted key files
	Command    string `json:"command,omitempty"`    // run this instead of a shell (console servers)
}

// Normalize fills defaults and validates per transport. It mutates the
// receiver so callers see canonical values.
func (t *Target) Normalize() error {
	switch t.Transport {
	case TransportSerial:
		return t.Serial.Validate()
	case TransportTelnet:
		hp, err := normalizeHostPort(t.Port, "23")
		if err != nil {
			return err
		}
		t.Port = hp
		return nil
	case TransportSSH:
		if t.Username == "" {
			return fmt.Errorf("ssh requires username")
		}
		hp, err := normalizeHostPort(t.Port, "22")
		if err != nil {
			return err
		}
		t.Port = hp
		if t.KeyPath == "" && t.Password == "" {
			return fmt.Errorf("ssh requires key_path or password")
		}
		return nil
	case "":
		return fmt.Errorf("transport must not be empty")
	default:
		return fmt.Errorf("unsupported transport %q", t.Transport)
	}
}

// Display renders a human summary for the open tool response.
func (t *Target) Display() string {
	switch t.Transport {
	case TransportSerial:
		s := t.Serial
		return fmt.Sprintf("%d baud, %d%d%s, no flow",
			s.BaudRate, s.DataBits, s.StopBits, strings.ToUpper(s.Parity[:1]))
	case TransportTelnet:
		return "telnet " + t.Port
	case TransportSSH:
		if t.Command != "" {
			return fmt.Sprintf("ssh %s@%s (%s)", t.Username, t.Port, t.Command)
		}
		return fmt.Sprintf("ssh %s@%s", t.Username, t.Port)
	default:
		return string(t.Transport)
	}
}

func normalizeHostPort(addr, defaultPort string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("host must not be empty")
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr, nil
	}
	// No port given: append the default. IPv6 literals need brackets.
	if strings.Count(addr, ":") > 1 && !strings.HasPrefix(addr, "[") {
		return "[" + addr + "]:" + defaultPort, nil
	}
	return net.JoinHostPort(addr, defaultPort), nil
}

const MaxBaudRate = 4_000_000

// Validate checks and default-fills serial line settings.
func (c *SerialConfig) Validate() error {
	if c.Port == "" {
		return fmt.Errorf("port path must not be empty")
	}
	if c.BaudRate <= 0 || c.BaudRate > MaxBaudRate {
		return fmt.Errorf("baud_rate must be between 1 and %d", MaxBaudRate)
	}
	if c.DataBits == 0 {
		c.DataBits = 8
	}
	if c.DataBits < 5 || c.DataBits > 8 {
		return fmt.Errorf("data_bits must be 5, 6, 7 or 8, got %d", c.DataBits)
	}
	if c.StopBits == 0 {
		c.StopBits = 1
	}
	if c.StopBits != 1 && c.StopBits != 2 {
		return fmt.Errorf("stop_bits must be 1 or 2, got %d", c.StopBits)
	}
	switch c.Parity {
	case "":
		c.Parity = "none"
	case "none", "odd", "even":
	default:
		return fmt.Errorf("parity must be none, odd or even, got %q", c.Parity)
	}
	switch c.FlowControl {
	case "":
		c.FlowControl = "none"
	case "none":
	case "hardware", "software":
		return fmt.Errorf("flow_control %q is not supported by go.bug.st/serial v1; use \"none\"", c.FlowControl)
	default:
		return fmt.Errorf("flow_control must be none, hardware or software, got %q", c.FlowControl)
	}
	return nil
}
