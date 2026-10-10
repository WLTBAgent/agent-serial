package serial

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func TestRingFIFO(t *testing.T) {
	r := NewRing(16)
	r.Write([]byte("hello"))
	r.Write([]byte(" world"))

	buf := make([]byte, 32)
	n := r.Read(buf)
	if got := string(buf[:n]); got != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
	if r.Len() != 0 {
		t.Fatalf("ring not drained: %d bytes left", r.Len())
	}
}

func TestRingDropsOldest(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("0123456789abcdef")) // 16 bytes into 8

	buf := make([]byte, 8)
	n := r.Read(buf)
	if got := string(buf[:n]); got != "89abcdef" {
		t.Fatalf("got %q, want newest 8 bytes %q", got, "89abcdef")
	}
	if r.Dropped() != 8 {
		t.Fatalf("dropped = %d, want 8", r.Dropped())
	}
}

func TestRingPartialOverflow(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("12345")) // 5 buffered
	r.Write([]byte("6789"))  // overflows by 1

	buf := make([]byte, 8)
	n := r.Read(buf)
	if got := string(buf[:n]); got != "23456789" {
		t.Fatalf("got %q, want %q", got, "23456789")
	}
	if r.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", r.Dropped())
	}
}

func TestRingPeekDoesNotConsume(t *testing.T) {
	r := NewRing(16)
	r.Write([]byte("abc"))

	buf := make([]byte, 16)
	if n := r.Peek(buf); string(buf[:n]) != "abc" {
		t.Fatalf("peek got %q", buf[:n])
	}
	if r.Len() != 3 {
		t.Fatalf("peek consumed data: %d left", r.Len())
	}
	if n := r.Read(buf); string(buf[:n]) != "abc" {
		t.Fatalf("read after peek got %q", buf[:n])
	}
}

func TestRingConcurrent(t *testing.T) {
	const total = 100_000
	// Capacity exceeds the total write volume: no byte is dropped, so the
	// reader can account for every byte written.
	r := NewRing(1 << 20)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			r.Write([]byte{byte(i)})
		}
	}()
	got := 0
	go func() {
		defer wg.Done()
		buf := make([]byte, 512)
		for got < total {
			if n := r.Read(buf); n == 0 {
				<-r.WaitCh()
				continue
			} else {
				got += n
			}
		}
	}()
	wg.Wait()
	if got != total {
		t.Fatalf("read %d bytes, want %d", got, total)
	}
	if dropped := r.Dropped(); dropped != 0 {
		t.Fatalf("dropped %d bytes, want 0", dropped)
	}
}

func TestCaptureDrain(t *testing.T) {
	r := NewRing(64)
	r.Write([]byte("hello"))

	res, err := r.Capture(context.Background(), CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "hello" || res.Reason != Drained {
		t.Fatalf("got %q reason %s", res.Data, res.Reason)
	}

	res, _ = r.Capture(context.Background(), CaptureOptions{})
	if len(res.Data) != 0 {
		t.Fatalf("second drain returned %q, want empty", res.Data)
	}
}

func TestCaptureDrainRespectsMaxBytes(t *testing.T) {
	r := NewRing(64)
	r.Write([]byte("0123456789"))

	res, _ := r.Capture(context.Background(), CaptureOptions{MaxBytes: 3})
	if string(res.Data) != "012" || res.Reason != Drained {
		t.Fatalf("got %q reason %s", res.Data, res.Reason)
	}
}

func TestCaptureInitialTimeout(t *testing.T) {
	r := NewRing(64)

	start := time.Now()
	res, err := r.Capture(context.Background(), CaptureOptions{Duration: 80 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if res.Reason != InitialTimeout || len(res.Data) != 0 {
		t.Fatalf("reason %s data %q, want initial_timeout and no data", res.Reason, res.Data)
	}
	if elapsed < 70*time.Millisecond {
		t.Fatalf("returned too early: %s", elapsed)
	}
}

func TestCaptureInitialTimeoutStopsBeforeDuration(t *testing.T) {
	r := NewRing(64)

	start := time.Now()
	// Initial window shorter than duration: silence ends the capture at the
	// initial deadline, not the duration deadline.
	res, err := r.Capture(context.Background(), CaptureOptions{
		Duration:       500 * time.Millisecond,
		InitialTimeout: 60 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if res.Reason != InitialTimeout {
		t.Fatalf("reason %s, want initial_timeout", res.Reason)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("silence should end capture at the initial deadline, took %s", elapsed)
	}
}

func TestCaptureDurationFromFirstByte(t *testing.T) {
	r := NewRing(64)
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.Write([]byte("data"))
	}()

	start := time.Now()
	res, err := r.Capture(context.Background(), CaptureOptions{Duration: 70 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if string(res.Data) != "data" || res.Reason != DurationElapsed {
		t.Fatalf("got %q reason %s", res.Data, res.Reason)
	}
	if res.Waited == 0 {
		t.Fatal("waited_ms not recorded")
	}
	// Duration runs from the first byte, so total ≈ 20ms wait + 70ms window.
	if elapsed < 85*time.Millisecond {
		t.Fatalf("ended too early at %s; duration must run from the first byte", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("took too long: %s", elapsed)
	}
}

func TestCaptureIdleTimeout(t *testing.T) {
	r := NewRing(64)
	go func() {
		time.Sleep(10 * time.Millisecond)
		r.Write([]byte("burst"))
	}()

	start := time.Now()
	res, err := r.Capture(context.Background(), CaptureOptions{
		Duration:    600 * time.Millisecond,
		IdleTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if string(res.Data) != "burst" || res.Reason != IdleTimeout {
		t.Fatalf("got %q reason %s", res.Data, res.Reason)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("idle cutoff should end the capture well before the window, took %s", elapsed)
	}
}

func TestCaptureMaxBytes(t *testing.T) {
	r := NewRing(64)
	go func() {
		time.Sleep(10 * time.Millisecond)
		r.Write([]byte("abcdef"))
	}()

	res, err := r.Capture(context.Background(), CaptureOptions{
		Duration: 400 * time.Millisecond,
		MaxBytes: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "abc" || res.Reason != MaxBytes {
		t.Fatalf("got %q reason %s", res.Data, res.Reason)
	}
}

func TestCaptureChunkOffsets(t *testing.T) {
	r := NewRing(64)
	go func() {
		r.Write([]byte("one"))
		time.Sleep(30 * time.Millisecond)
		r.Write([]byte("four"))
	}()

	res, err := r.Capture(context.Background(), CaptureOptions{
		Duration:    300 * time.Millisecond,
		IdleTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(res.Data), "onefour"; got != want {
		t.Fatalf("data %q, want %q", got, want)
	}
	if len(res.Chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(res.Chunks))
	}
	if res.Chunks[0].Offset != 0 || len(res.Chunks[0].Data) != 3 {
		t.Fatalf("chunk 0: offset %d len %d", res.Chunks[0].Offset, len(res.Chunks[0].Data))
	}
	if res.Chunks[1].Offset != 3 || len(res.Chunks[1].Data) != 4 {
		t.Fatalf("chunk 1: offset %d len %d", res.Chunks[1].Offset, len(res.Chunks[1].Data))
	}
	if !bytes.Equal(res.Data, append(append([]byte{}, res.Chunks[0].Data...), res.Chunks[1].Data...)) {
		t.Fatal("chunks do not concatenate to data")
	}
}

func TestCaptureContextCancel(t *testing.T) {
	r := NewRing(64)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := r.Capture(ctx, CaptureOptions{Duration: time.Second})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}
