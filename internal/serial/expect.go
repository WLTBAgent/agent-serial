package serial

import (
	"bytes"
	"context"
	"fmt"
	"time"
)

// Matched completes the capture completion reasons: an Expect reached its
// max_hits.
const Matched CompletionReason = "matched"

// ExpectOptions configures a pattern-triggered wait: collect output, and
// each time Pattern appears, optionally send a payload — at most MaxHits
// times. Reaching MaxHits ends the call immediately, so a pattern that
// recurs (boot loop, periodic banner) cannot retrigger the send.
type ExpectOptions struct {
	Pattern   []byte        // required, non-empty
	Send      []byte        // written once per hit; nil = never send (wait only)
	SendDelay time.Duration // wait this long after a match before sending

	MaxHits  int // default 1
	MaxBytes int // cap on collected data, default 64 KiB

	// Deadline scaffolding mirrors CaptureOptions. Duration (default 10s,
	// measured from the first byte) and InitialTimeout (default: Duration,
	// bounds the wait for the first byte) always apply; IdleTimeout (0 =
	// off) can end the wait early when data flowed but the pattern never
	// appeared.
	Duration       time.Duration
	InitialTimeout time.Duration
	IdleTimeout    time.Duration
}

// ExpectResult is the outcome of an Expect call. Data holds everything
// received during the wait (matched text included); MatchOffsets mark where
// each hit begins within Data.
type ExpectResult struct {
	Data         []byte
	Reason       CompletionReason
	Hits         int
	MatchOffsets []int
	Sent         int // total payload bytes written (hits × len(Send))
	Waited       time.Duration
	Elapsed      time.Duration
}

// Expect scans the receive stream for opts.Pattern. On each hit it records
// the match, sends opts.Send (if set), and once hits reach MaxHits returns
// with reason Matched. Output received after the final send stays in the
// ring for later reads.
func (c *Connection) Expect(ctx context.Context, opts ExpectOptions) (*ExpectResult, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	if len(opts.Pattern) == 0 {
		return nil, fmt.Errorf("pattern must not be empty")
	}
	if opts.MaxHits <= 0 {
		opts.MaxHits = 1
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 64 * 1024
	}
	if opts.Duration <= 0 {
		opts.Duration = 10 * time.Second
	}

	res := &ExpectResult{}
	start := time.Now()

	initial := opts.InitialTimeout
	if initial <= 0 || initial > opts.Duration {
		initial = opts.Duration
	}
	initialDeadline := start.Add(initial)
	var hardDeadline time.Time // armed by the first byte
	var idleDeadline time.Time // armed after every byte when IdleTimeout > 0

	buf := make([]byte, 512)
	scanned := 0 // res.Data prefix already searched

	for {
		// Pull whatever arrived into the collected data.
		before := len(res.Data)
		for len(res.Data) < opts.MaxBytes {
			n := c.ring.Read(buf[:min(cap(buf), opts.MaxBytes-len(res.Data))])
			if n == 0 {
				break
			}
			res.Data = append(res.Data, buf[:n]...)
		}
		if len(res.Data) > before {
			now := time.Now()
			if hardDeadline.IsZero() {
				res.Waited = now.Sub(start)
				hardDeadline = now.Add(opts.Duration)
			}
			if opts.IdleTimeout > 0 {
				idleDeadline = now.Add(opts.IdleTimeout)
			}
		}

		// Rescan from one pattern-length back, so matches split across
		// arrivals are caught exactly once.
		searchFrom := scanned - (len(opts.Pattern) - 1)
		if searchFrom < 0 {
			searchFrom = 0
		}
		matched, err := expectScan(ctx, c, &opts, res, searchFrom)
		if err != nil || matched {
			res.Elapsed = time.Since(start)
			return res, err
		}
		scanned = len(res.Data)

		// Deadline handling, mirroring Capture.
		now := time.Now()
		if hardDeadline.IsZero() {
			if now.Before(initialDeadline) {
				if err := c.ring.wait(ctx, initialDeadline); err != nil {
					res.Elapsed = time.Since(start)
					return res, err
				}
				continue
			}
			res.Reason = InitialTimeout
			res.Elapsed = time.Since(start)
			return res, nil
		}
		stop := hardDeadline
		reason := DurationElapsed
		if !idleDeadline.IsZero() && idleDeadline.Before(stop) {
			stop = idleDeadline
			reason = IdleTimeout
		}
		if now.Before(stop) {
			if err := c.ring.wait(ctx, stop); err != nil {
				res.Elapsed = time.Since(start)
				return res, err
			}
			continue
		}
		res.Reason = reason
		res.Elapsed = time.Since(start)
		return res, nil
	}
}

// expectScan finds non-overlapping Pattern occurrences in res.Data from
// searchFrom onward. Each new hit is recorded and answered; the scan ends
// the whole Expect when hits reach MaxHits.
func expectScan(ctx context.Context, c *Connection, opts *ExpectOptions, res *ExpectResult, searchFrom int) (bool, error) {
	for {
		i := bytes.Index(res.Data[searchFrom:], opts.Pattern)
		if i < 0 {
			return false, nil
		}
		at := searchFrom + i
		searchFrom = at + len(opts.Pattern) // non-overlapping

		res.Hits++
		res.MatchOffsets = append(res.MatchOffsets, at)

		if len(opts.Send) > 0 {
			if err := expectSend(ctx, c, opts, res); err != nil {
				return true, err
			}
		}
		if res.Hits >= opts.MaxHits {
			res.Reason = Matched
			return true, nil
		}
	}
}

func expectSend(ctx context.Context, c *Connection, opts *ExpectOptions, res *ExpectResult) error {
	if opts.SendDelay > 0 {
		timer := time.NewTimer(opts.SendDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	n, err := c.Write(opts.Send)
	res.Sent += n
	return err
}
