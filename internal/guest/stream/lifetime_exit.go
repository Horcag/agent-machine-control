package stream

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrLifetimePending      = errors.New("guest stream: lifetime query pending")
	ErrLifetimeQuery        = errors.New("guest stream: lifetime query failed")
	ErrLifetimeInformation  = errors.New("guest stream: lifetime information incomplete")
	ErrLifetimeDisconnected = errors.New("guest stream: lifetime pipe disconnected")
)

func observeLifetimeExit(ctx context.Context, channel io.ReadWriteCloser, err error) error {
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	observer, ok := channel.(interface {
		ExitStatus(context.Context) (int, bool)
	})
	if !ok || ctx.Err() != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	code, known := observer.ExitStatus(bounded)
	if !known || bounded.Err() != nil {
		return err
	}
	var marker error
	switch code {
	case 211:
		marker = ErrLifetimeQuery
	case 212:
		marker = ErrLifetimeInformation
	case 213:
		marker = ErrLifetimeDisconnected
	case 214:
		marker = ErrLifetimePending
	default:
		return err
	}
	return errors.Join(err, marker)
}
