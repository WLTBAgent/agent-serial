package serial

import (
	"bytes"

	"net"
	"testing"
	"time"
)

// startTelnetServer accepts one connection and runs fn with it.
func startTelnetServer(t *testing.T, fn func(conn net.Conn, incoming <-chan []byte)) (string, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	incoming := make(chan []byte, 16)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := conn.Read(buf)
				if n > 0 {
					incoming <- append([]byte(nil), buf[:n]...)
				}
				if err != nil {
					return
				}
			}
		}()
		fn(conn, incoming)
	}()
	return listener.Addr().String(), incoming
}

func waitForChannelData(t *testing.T, ch <-chan []byte, want []byte, what string) []byte {
	t.Helper()
	deadline := time.After(3 * time.Second)
	var acc []byte
	for {
		select {
		case got := <-ch:
			acc = append(acc, got...)
			if bytes.Contains(acc, want) {
				return acc
			}
		case <-deadline:
			t.Fatalf("%s: got %x so far, want %x", what, acc, want)
		}
	}
}

func TestTelnetRefusesNegotiationAndDeliversData(t *testing.T) {
	addr, incoming := startTelnetServer(t, func(conn net.Conn, _ <-chan []byte) {
		// WILL ECHO + banner.
		conn.Write([]byte{iac, willByte, 1})
		conn.Write([]byte("welcome to console\n"))
		time.Sleep(2 * time.Second) // hold the conn open for the refusal
	})

	stream, err := openTelnetStream(Target{Transport: TransportTelnet, Port: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var readAcc []byte
	// Reading triggers the negotiation parse (the banner carries the WILL).
	buf := make([]byte, 128)
	deadline := time.Now().Add(3 * time.Second)
	for !bytes.Contains(readAcc, []byte("welcome")) && time.Now().Before(deadline) {
		n, err := stream.Read(buf)
		readAcc = append(readAcc, buf[:n]...)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, readAcc)
		}
	}

	// The server must have received IAC DONT ECHO.
	waitForChannelData(t, incoming, []byte{iac, dontByte, 1}, "negotiation refusal")
}

func TestTelnetStripsIACFromRead(t *testing.T) {
	addr, _ := startTelnetServer(t, func(conn net.Conn, _ <-chan []byte) {
		conn.Write([]byte{iac, willByte, 1}) // WILL ECHO → refused
		conn.Write([]byte("u"))              // data arrives split
		conn.Write([]byte{iac, doByte, 3})   // DO SUPPRESS-GA → refused
		conn.Write([]byte("ser"))
		conn.Write([]byte{iac, sbByte, 24, 0, iac, seByte}) // subneg to skip
		conn.Write([]byte("@host"))
	})

	stream, err := openTelnetStream(Target{Transport: TransportTelnet, Port: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	deadline := time.Now().Add(3 * time.Second)
	var acc []byte
	buf := make([]byte, 256)
	for !bytes.Contains(acc, []byte("user@host")) && time.Now().Before(deadline) {
		n, err := stream.Read(buf)
		acc = append(acc, buf[:n]...)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, acc)
		}
	}
	if !bytes.Equal(acc, []byte("user@host")) {
		t.Fatalf("stream = %q, want clean \"user@host\" with IAC stripped", acc)
	}
}

func TestTelnetHandlesSequenceSplitAcrossReads(t *testing.T) {
	// Feed the IAC and its command byte through separate TCP writes with a
	// pause in between, so the parser must buffer the partial sequence.
	addr, _ := startTelnetServer(t, func(conn net.Conn, _ <-chan []byte) {
		conn.Write([]byte{iac})
		time.Sleep(30 * time.Millisecond)
		conn.Write([]byte{willByte})
		time.Sleep(30 * time.Millisecond)
		conn.Write([]byte{31}) // option 31
		conn.Write([]byte("after-nego"))
		time.Sleep(200 * time.Millisecond)
	})

	stream, err := openTelnetStream(Target{Transport: TransportTelnet, Port: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	deadline := time.Now().Add(3 * time.Second)
	var acc []byte
	buf := make([]byte, 256)
	for !bytes.Contains(acc, []byte("after-nego")) && time.Now().Before(deadline) {
		n, err := stream.Read(buf)
		acc = append(acc, buf[:n]...)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, acc)
		}
	}
	if bytes.Contains(acc, []byte{willByte}) {
		t.Fatalf("command byte leaked into data: %q", acc)
	}
	if !bytes.Equal(acc, []byte("after-nego")) {
		t.Fatalf("stream = %q", acc)
	}
}

func TestTelnetWriteEscapesFF(t *testing.T) {
	addr, incoming := startTelnetServer(t, func(conn net.Conn, _ <-chan []byte) {
		time.Sleep(2 * time.Second) // helper reader goroutine forwards writes
	})

	stream, err := openTelnetStream(Target{Transport: TransportTelnet, Port: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if _, err := stream.Write([]byte{0xff, 0x03, 'x'}); err != nil {
		t.Fatal(err)
	}
	got := waitForChannelData(t, incoming, []byte{iac, iac, 0x03}, "escaped write")
	if !bytes.Equal(got, []byte{iac, iac, 0x03, 'x'}) {
		t.Fatalf("server got %x, want escaped ff and untouched rest", got)
	}
}
