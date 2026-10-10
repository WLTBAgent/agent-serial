package serial

import (
	"context"
	"testing"
	"time"
)

func newTestRegistry(opener *fakeOpener, mutate func(*RegistryOptions)) *Registry {
	opts := RegistryOptions{RingCapacity: 4096}
	if mutate != nil {
		mutate(&opts)
	}
	return NewRegistryWithOpener(opts, opener.open)
}

func TestRegistryOpenWriteReadClose(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)

	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}

	// Write flows to the port.
	if n, err := conn.Write([]byte("ping\n")); err != nil || n != 5 {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	if got := string(opener.get("/dev/ttyFAKE0").Written()); got != "ping\n" {
		t.Fatalf("port got %q", got)
	}

	// Device output flows to the ring via the background reader.
	opener.get("/dev/ttyFAKE0").Feed([]byte("pong\n"))
	deadline := time.Now().Add(2 * time.Second)
	for conn.Ring().Len() < 5 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	data, err := conn.Peek(1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "pong\n" {
		t.Fatalf("ring has %q, want %q", data, "pong\n")
	}

	// Control lines.
	if err := conn.SetDTR(false); err != nil {
		t.Fatal(err)
	}
	if _, dtr := opener.get("/dev/ttyFAKE0").Lines(); dtr {
		t.Fatal("DTR should be false")
	}

	if err := reg.Close(conn.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Get(conn.ID()); err == nil {
		t.Fatal("closed connection still in registry")
	}
}

func TestRegistryRejectsDuplicatePort(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)

	if _, err := reg.Open(validTarget("/dev/ttyFAKE0")); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Open(validTarget("/dev/ttyFAKE0")); err == nil {
		t.Fatal("expected duplicate-port refusal")
	}
}

func TestRegistryMaxConnections(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, func(o *RegistryOptions) { o.MaxConnections = 1 })

	if _, err := reg.Open(validTarget("/dev/ttyFAKE0")); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Open(validTarget("/dev/ttyFAKE1")); err == nil {
		t.Fatal("expected connection-limit refusal")
	}
}

func TestRegistryAllowBlockLists(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, func(o *RegistryOptions) {
		o.AllowedPorts = []string{"ttyUSB"}
		o.BlockedPorts = []string{"ttyUSB9"}
	})

	if _, err := reg.Open(validTarget("/dev/ttyUSB0")); err != nil {
		t.Fatalf("allowed port refused: %v", err)
	}
	if _, err := reg.Open(validTarget("/dev/ttyACM0")); err == nil {
		t.Fatal("port outside allow list was opened")
	}
	if _, err := reg.Open(validTarget("/dev/ttyUSB9")); err == nil {
		t.Fatal("blocked port was opened")
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*SerialConfig)
		wantErr bool
	}{
		{"defaults", func(c *SerialConfig) {}, false},
		{"zero baud", func(c *SerialConfig) { c.BaudRate = 0 }, true},
		{"huge baud", func(c *SerialConfig) { c.BaudRate = 5_000_000 }, true},
		{"bad data bits", func(c *SerialConfig) { c.DataBits = 9 }, true},
		{"bad stop bits", func(c *SerialConfig) { c.StopBits = 3 }, true},
		{"bad parity", func(c *SerialConfig) { c.Parity = "mark" }, true},
		{"hardware flow", func(c *SerialConfig) { c.FlowControl = "hardware" }, true},
		{"empty port", func(c *SerialConfig) { c.Port = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := SerialConfig{Port: "/dev/ttyUSB0", BaudRate: 115200}
			tc.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestConnectionSingleReader(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(context.Background(), CaptureOptions{Duration: 200 * time.Millisecond})
		done <- err
	}()

	// Give the first read time to take the capture lock, then try a second.
	time.Sleep(20 * time.Millisecond)
	if _, err := conn.Read(context.Background(), CaptureOptions{Duration: 10 * time.Millisecond}); err == nil {
		t.Fatal("concurrent read should be refused")
	}
	if err := <-done; err != nil {
		t.Fatalf("first read failed: %v", err)
	}
}

func TestConnectionWriteAfterClose(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Close(conn.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Fatal("write after close should fail")
	}
}

func TestEncodeDecode(t *testing.T) {
	enc, err := NormalizeEncoding("UTF-8")
	if err != nil || enc != EncodingUTF8 {
		t.Fatalf("normalize: %q %v", enc, err)
	}

	got, _ := EncodeData([]byte{0xde, 0xad, 0x01}, EncodingHex)
	if got != "de ad 01" {
		t.Fatalf("hex encode: %q", got)
	}
	back, err := DecodeData("DE AD 01", EncodingHex)
	if err != nil || !equalBytes(back, []byte{0xde, 0xad, 0x01}) {
		t.Fatalf("hex decode: %x %v", back, err)
	}

	got, _ = EncodeData([]byte{0xde, 0xad}, EncodingBase64)
	if back, err = DecodeData(got, EncodingBase64); err != nil || !equalBytes(back, []byte{0xde, 0xad}) {
		t.Fatalf("base64 roundtrip: %q %x %v", got, back, err)
	}

	// Invalid UTF-8 is substituted, not dropped.
	got, _ = EncodeData([]byte{'o', 'k', 0xff}, EncodingUTF8)
	if got != "ok\ufffd" {
		t.Fatalf("lossy utf8: %q", got)
	}

	if _, err := DecodeData("nothex!", EncodingHex); err == nil {
		t.Fatal("invalid hex accepted")
	}
	if _, err := NormalizeEncoding("rot13"); err == nil {
		t.Fatal("invalid encoding accepted")
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
