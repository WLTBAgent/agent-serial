package serial

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startSSHServer runs an in-process SSH server with password auth and a
// session channel that echoes stdin back to stdout. Returns its address.
func startSSHServer(t *testing.T, acceptPassword string) string {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == acceptPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("wrong password")
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(conn, config)
		}
	}()
	return listener.Addr().String()
}

func serveSSHConn(conn net.Conn, config *ssh.ServerConfig) {
	serverConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			return
		}
		go serveSSHSession(ch, chReqs)
	}
}

// serveSSHSession approves PTY/shell requests and echoes stdin to stdout,
// which is all our transport tests need.
func serveSSHSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req", "shell", "exec":
			req.Reply(true, nil)
			if req.Type != "pty-req" {
				go func() {
					buf := make([]byte, 1024)
					for {
						n, err := ch.Read(buf)
						if n > 0 {
							ch.Write(buf[:n])
						}
						if err != nil {
							return
						}
					}
				}()
			}
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func sshTestTarget(addr string) Target {
	return Target{
		Transport: TransportSSH,
		Port:      addr,
		Username:  "operator",
		Password:  "secret",
	}
}

func TestSSHRoundtripAndTOFUPinning(t *testing.T) {
	addr := startSSHServer(t, "secret")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	stream, err := openSSHStream(sshTestTarget(addr), knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// First connect must have pinned the host key.
	pinned, err := os.ReadFile(knownHosts)
	if err != nil || len(pinned) == 0 {
		t.Fatalf("host key not pinned on first use: %v", err)
	}

	if _, err := stream.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	deadline := time.Now().Add(3 * time.Second)
	var acc []byte
	for !bytes.Contains(acc, []byte("ping")) && time.Now().Before(deadline) {
		n, err := stream.Read(buf)
		acc = append(acc, buf[:n]...)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, acc)
		}
	}
}

func TestSSHPinsSecondHostIndependently(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	// Two servers at different addresses each get their own pinned entry;
	// neither should trip the mismatch check.
	for range 2 {
		addr := startSSHServer(t, "secret")
		stream, err := openSSHStream(sshTestTarget(addr), knownHosts)
		if err != nil {
			t.Fatal(err)
		}
		stream.Close()
	}
	entries, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(bytes.Split(bytes.TrimRight(entries, "\n"), []byte("\n"))); got != 2 {
		t.Fatalf("known_hosts has %d entries, want 2", got)
	}
}

func TestSSHRejectsCorruptKnownHosts(t *testing.T) {
	addr := startSSHServer(t, "secret")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("junk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openSSHStream(sshTestTarget(addr), knownHosts); err == nil {
		t.Fatal("connect with corrupted known_hosts should fail")
	}
}

func TestSSHRejectsBadPassword(t *testing.T) {
	addr := startSSHServer(t, "secret")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	target := sshTestTarget(addr)
	target.Password = "wrong"
	if _, err := openSSHStream(target, knownHosts); err == nil {
		t.Fatal("bad password should be refused")
	}
}
