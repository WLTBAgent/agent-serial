package serial

import (
	"context"
	"time"
)

// CompletionReason explains why a capture or drain returned.
type CompletionReason string

const (
	// Drained means no capture window was requested; whatever was buffered
	// at call time was returned (possibly zero bytes).
	Drained CompletionReason = "drained"
	// DurationElapsed means the capture ran its full duration. The duration
	// is measured from the first byte, or from capture start if the ring
	// already held data.
	DurationElapsed CompletionReason = "duration_elapsed"
	// InitialTimeout means no byte arrived within the initial window, which
	// starts at capture start and defaults to the full duration.
	InitialTimeout CompletionReason = "initial_timeout"
	// IdleTimeout means the line went silent for the idle timeout after the
	// most recent byte.
	IdleTimeout CompletionReason = "idle_timeout"
	// MaxBytes means the capture stopped because the byte limit was reached.
	MaxBytes CompletionReason = "max_bytes"
)

// CaptureOptions controls a single read from a Ring.
//
// Duration == 0 (the default) means "drain": return whatever is buffered
// right now without waiting. Duration > 0 opens a capture window: wait for
// the first byte (up to InitialTimeout, defaulting to Duration), then keep
// collecting for at least Duration measured from that first byte, stopping
// early on an idle gap (IdleTimeout) or the byte limit.
type CaptureOptions struct {
	MaxBytes       int
	Duration       time.Duration
	InitialTimeout time.Duration // max wait for the first byte; defaults to Duration
	IdleTimeout    time.Duration // silence cutoff after most recent byte; 0 disables
}

// Chunk is a contiguous run of bytes at a byte offset within the capture.
type Chunk struct {
	Data   []byte `json:"data"`
	Offset int    `json:"offset"`
}

// CaptureResult is the outcome of a capture.
type CaptureResult struct {
	Data    []byte
	Chunks  []Chunk
	Reason  CompletionReason
	Waited  time.Duration // time from capture start to first byte
	Elapsed time.Duration // total capture wall time
}

// Capture reads from the ring per opts. It always returns the bytes it
// collected; the only error is ctx cancellation.
func (r *Ring) Capture(ctx context.Context, opts CaptureOptions) (*CaptureResult, error) {
	start := time.Now()
	res := &CaptureResult{Reason: Drained}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}

	buf := make([]byte, 512)
	appendChunk := func(data []byte) {
		res.Chunks = append(res.Chunks, Chunk{Data: append([]byte(nil), data...), Offset: len(res.Data)})
		res.Data = append(res.Data, data...)
	}

	if opts.Duration <= 0 {
		for len(res.Data) < maxBytes {
			n := r.Read(buf[:min(cap(buf), maxBytes-len(res.Data))])
			if n == 0 {
				break
			}
			appendChunk(buf[:n])
		}
		res.Elapsed = time.Since(start)
		return res, nil
	}

	duration := opts.Duration
	initial := opts.InitialTimeout
	if initial <= 0 || initial > duration {
		initial = duration
	}
	idle := opts.IdleTimeout

	initialDeadline := start.Add(initial)
	var hardDeadline time.Time // armed by the first byte
	var idleDeadline time.Time // armed after every byte

	for {
		n := r.Read(buf[:min(cap(buf), maxBytes-len(res.Data))])
		if n > 0 {
			now := time.Now()
			if len(res.Data) == 0 {
				res.Waited = now.Sub(start)
				hardDeadline = now.Add(duration)
			}
			appendChunk(buf[:n])
			if idle > 0 {
				idleDeadline = now.Add(idle)
			}
			if len(res.Data) >= maxBytes {
				res.Reason = MaxBytes
				break
			}
		}

		now := time.Now()

		// Still waiting for the first byte.
		if hardDeadline.IsZero() {
			if now.Before(initialDeadline) {
				if err := r.wait(ctx, initialDeadline); err != nil {
					return res, err
				}
				continue
			}
			res.Reason = InitialTimeout
			break
		}

		// Data is flowing: stop at whichever deadline expires first.
		stop := hardDeadline
		reason := DurationElapsed
		if !idleDeadline.IsZero() && idleDeadline.Before(stop) {
			stop = idleDeadline
			reason = IdleTimeout
		}
		if now.Before(stop) {
			if err := r.wait(ctx, stop); err != nil {
				return res, err
			}
			continue
		}
		res.Reason = reason
		break
	}

	res.Elapsed = time.Since(start)
	return res, nil
}

// wait blocks until data arrives, the context is done, or the deadline passes.
func (r *Ring) wait(ctx context.Context, deadline time.Time) error {
	d := time.Until(deadline)
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.WaitCh():
		return nil
	case <-timer.C:
		return nil
	}
}
