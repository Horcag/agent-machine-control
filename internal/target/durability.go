package target

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
)

func (s *Store) repairPending(ctx context.Context) error {
	if s.pending == nil {
		return nil
	}
	var err error
	if s.pending.kind == pendingClear {
		_, err = s.repairCleared(ctx)
	} else {
		_, err = s.repairSaved(ctx, s.pending.value, s.pending.payload)
	}
	return err
}

func (s *Store) repairSaved(ctx context.Context, value Default, payload []byte) (Publication, error) {
	publication := Publication{Committed: true}
	if err := ctx.Err(); err != nil {
		s.rememberSave(value, payload)
		return publication, errors.Join(ErrCommittedNotDurable, err)
	}
	if err := s.operations.SyncDir(s.dir); err != nil {
		s.rememberSave(value, payload)
		return publication, fmt.Errorf("%w: directory sync: %v", ErrCommittedNotDurable, err)
	}
	readBack, err := s.operations.ReadFile(ctx, s.path)
	if err != nil || !bytes.Equal(readBack, payload) {
		s.rememberSave(value, payload)
		if err == nil {
			err = errors.New("canonical read-back mismatch")
		}
		return publication, fmt.Errorf("%w: %v", ErrCommittedNotDurable, err)
	}
	decoded, err := decode(readBack)
	if err != nil || !decoded.equal(value) {
		s.rememberSave(value, payload)
		if err == nil {
			err = errors.New("canonical authority mismatch")
		}
		return publication, fmt.Errorf("%w: %v", ErrCommittedNotDurable, err)
	}
	s.pending = nil
	publication.Durable = true
	return publication, nil
}

func (s *Store) rememberSave(value Default, payload []byte) {
	s.pending = &pendingEffect{kind: pendingSave, value: value.Clone(), payload: bytes.Clone(payload)}
}

func (s *Store) repairCleared(ctx context.Context) (Publication, error) {
	publication := Publication{Committed: true}
	if err := ctx.Err(); err != nil {
		s.pending = &pendingEffect{kind: pendingClear}
		return publication, errors.Join(ErrCommittedNotDurable, err)
	}
	if err := s.operations.SyncDir(s.dir); err != nil {
		s.pending = &pendingEffect{kind: pendingClear}
		return publication, fmt.Errorf("%w: directory sync: %v", ErrCommittedNotDurable, err)
	}
	_, err := s.operations.ReadFile(ctx, s.path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		s.pending = &pendingEffect{kind: pendingClear}
		if err == nil {
			err = errors.New("cleared authority remains present")
		}
		return publication, fmt.Errorf("%w: %v", ErrCommittedNotDurable, err)
	}
	s.pending = nil
	publication.Durable = true
	return publication, nil
}
