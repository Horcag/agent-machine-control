package sessions

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// WaitSettle blocks until the session output remains quiet for settleDuration or context expires.
func WaitSettle(ctx context.Context, buf *RingBuffer, settleDuration time.Duration, afterSeq uint64, timeout time.Duration) ([]domain.SessionChunk, uint64, uint64, error) {
	if settleDuration <= 0 {
		settleDuration = domain.DefaultSettleTime
	}
	if timeout <= 0 || timeout > domain.MaxWaitDuration {
		timeout = domain.DefaultWaitTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	changeCh := make(chan struct{}, 10)
	unregister := buf.RegisterChangeChan(changeCh)
	defer unregister()

	timer := time.NewTimer(settleDuration)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, 0, 0, domain.ErrSessionWaitTimeout
			}
			return nil, 0, 0, ctx.Err()

		case <-changeCh:
			// Reset quiet timer on new activity
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(settleDuration)

		case <-timer.C:
			// Output has settled
			chunks, nextSeq, lossBytes, _ := buf.ReadAfter(afterSeq, DefaultMaxReadLimitBytes)
			return chunks, nextSeq, lossBytes, nil
		}
	}
}

// WaitRegex blocks until a regular expression matches the accumulated output since afterSeq or context expires.
func WaitRegex(ctx context.Context, buf *RingBuffer, pattern string, afterSeq uint64, timeout time.Duration) ([]domain.SessionChunk, uint64, uint64, bool, error) {
	if len(pattern) > domain.MaxSessionRegexPatternLength {
		return nil, 0, 0, false, fmt.Errorf("%w: regex pattern exceeds maximum length (%d)", domain.ErrNonCanonicalParameter, domain.MaxSessionRegexPatternLength)
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("%w: invalid regex: %v", domain.ErrNonCanonicalParameter, err)
	}

	if timeout <= 0 || timeout > domain.MaxWaitDuration {
		timeout = domain.DefaultWaitTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	changeCh := make(chan struct{}, 10)
	unregister := buf.RegisterChangeChan(changeCh)
	defer unregister()

	checkMatch := func() ([]domain.SessionChunk, uint64, uint64, bool) {
		chunks, nextSeq, lossBytes, _ := buf.ReadAfter(afterSeq, DefaultMaxReadLimitBytes)
		var combined strings.Builder
		for _, c := range chunks {
			combined.WriteString(c.Data)
		}
		if re.MatchString(combined.String()) {
			return chunks, nextSeq, lossBytes, true
		}
		return chunks, nextSeq, lossBytes, false
	}

	// 1. Check existing buffer first
	chunks, nextSeq, lossBytes, matched := checkMatch()
	if matched {
		return chunks, nextSeq, lossBytes, true, nil
	}

	// 2. Wait for incoming chunks
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, 0, 0, false, domain.ErrSessionWaitTimeout
			}
			return nil, 0, 0, false, ctx.Err()

		case <-changeCh:
			chunks, nextSeq, lossBytes, matched = checkMatch()
			if matched {
				return chunks, nextSeq, lossBytes, true, nil
			}
		}
	}
}
