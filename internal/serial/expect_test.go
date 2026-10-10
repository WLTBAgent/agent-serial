package serial

import (
	"context"
	"testing"
	"time"
)

func TestExpectImmediateMatchFromBuffer(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	// The banner already arrived before expect was called.
	opener.get("/dev/ttyFAKE0").Feed([]byte("Hit any key to stop autoboot: 3"))

	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:  []byte("autoboot"),
		Send:     []byte("\x03"),
		Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != Matched || res.Hits != 1 || res.Sent != 1 {
		t.Fatalf("reason %s hits %d sent %d", res.Reason, res.Hits, res.Sent)
	}
	if got := string(opener.get("/dev/ttyFAKE0").Written()); got != "\x03" {
		t.Fatalf("port got %q, want Ctrl-C", got)
	}
	if len(res.MatchOffsets) != 1 || res.MatchOffsets[0] != 20 {
		t.Fatalf("match offsets %v, want [20]", res.MatchOffsets)
	}
}

func TestExpectSplitAcrossArrivals(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	go func() {
		time.Sleep(10 * time.Millisecond)
		opener.get("/dev/ttyFAKE0").Feed([]byte("stop auto"))
		time.Sleep(30 * time.Millisecond)
		opener.get("/dev/ttyFAKE0").Feed([]byte("boot: 1"))
	}()

	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:  []byte("autoboot"),
		Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != Matched || res.Hits != 1 {
		t.Fatalf("reason %s hits %d", res.Reason, res.Hits)
	}
	if got := string(res.Data); got != "stop autoboot: 1" {
		t.Fatalf("data %q", got)
	}
}

func TestExpectMaxHitsLimitsRetrigger(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	// Boot-loop style: pattern appears three times at once.
	opener.get("/dev/ttyFAKE0").Feed([]byte("autoboot ... autoboot ... autoboot"))

	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:  []byte("autoboot"),
		Send:     []byte("\x03"),
		MaxHits:  2,
		Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != Matched || res.Hits != 2 || res.Sent != 2 {
		t.Fatalf("reason %s hits %d sent %d", res.Reason, res.Hits, res.Sent)
	}
	if len(res.MatchOffsets) != 2 || res.MatchOffsets[0] != 0 || res.MatchOffsets[1] != 13 {
		t.Fatalf("offsets %v, want [0 13]", res.MatchOffsets)
	}
}

func TestExpectDefaultMaxHitsSendsOnce(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	opener.get("/dev/ttyFAKE0").Feed([]byte("autoboot autoboot autoboot"))

	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:  []byte("autoboot"),
		Send:     []byte("\x03"),
		Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Hits != 1 || res.Sent != 1 {
		t.Fatalf("hits %d sent %d, want 1/1: recurring pattern must not re-trigger", res.Hits, res.Sent)
	}
}

func TestExpectWaitOnlyNoSend(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	opener.get("/dev/ttyFAKE0").Feed([]byte("login:"))

	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:  []byte("login:"),
		Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != Matched || res.Hits != 1 || res.Sent != 0 {
		t.Fatalf("reason %s hits %d sent %d", res.Reason, res.Hits, res.Sent)
	}
	if len(opener.get("/dev/ttyFAKE0").Written()) != 0 {
		t.Fatal("nothing should be written for wait-only expect")
	}
}

func TestExpectInitialTimeoutOnSilence(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	start := time.Now()
	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:  []byte("never"),
		Duration: 80 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != InitialTimeout || res.Hits != 0 {
		t.Fatalf("reason %s hits %d", res.Reason, res.Hits)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatalf("took too long: %s", time.Since(start))
	}
}

func TestExpectIdleTimeoutWhenPatternNeverComes(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	opener.get("/dev/ttyFAKE0").Feed([]byte("booting main os\n"))

	start := time.Now()
	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:     []byte("emergency shell"),
		Duration:    time.Second,
		IdleTimeout: 60 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != IdleTimeout {
		t.Fatalf("reason %s, want idle_timeout", res.Reason)
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatalf("idle cutoff should end the wait early, took %s", time.Since(start))
	}
}

func TestExpectSendDelay(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	opener.get("/dev/ttyFAKE0").Feed([]byte("match now"))

	start := time.Now()
	res, err := conn.Expect(context.Background(), ExpectOptions{
		Pattern:   []byte("match"),
		Send:      []byte("x"),
		SendDelay: 80 * time.Millisecond,
		Duration:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != Matched || res.Sent != 1 {
		t.Fatalf("reason %s sent %d", res.Reason, res.Sent)
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("send delay not honored: %s", elapsed)
	}
}

func TestExpectEmptyPatternRejected(t *testing.T) {
	opener := newFakeOpener()
	reg := newTestRegistry(opener, nil)
	conn, err := reg.Open(validTarget("/dev/ttyFAKE0"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()

	if _, err := conn.Expect(context.Background(), ExpectOptions{Pattern: nil}); err == nil {
		t.Fatal("empty pattern should be rejected")
	}
}
