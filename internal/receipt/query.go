package receipt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// Get reads and validates a single receipt by receipt ID.
func (s *Store) Get(receiptID string) (*domain.Receipt, error) {
	return s.GetContext(context.Background(), receiptID)
}

// GetContext reads and validates one receipt within the caller's deadline.
func (s *Store) GetContext(ctx context.Context, receiptID string) (*domain.Receipt, error) {
	if err := domain.ValidateReceiptID(receiptID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := filepath.Join(s.dir, fmt.Sprintf("%s.json", receiptID))
	rcpt, err := s.readReceiptFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrReceiptNotFound
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rcpt, nil
}

// List returns up to limit receipts, optionally filtered by actor, sorted by CompletedAt descending.
func (s *Store) List(limit int, actorFilter string) ([]domain.Receipt, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.Receipt{}, nil
		}
		return nil, fmt.Errorf("receipt: failed to read receipts directory: %w", err)
	}

	var results []domain.Receipt
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.Contains(entry.Name(), ".tmp.") {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		rcpt, err := s.readReceiptFile(filePath)
		if err != nil {
			return nil, err
		}

		if actorFilter != "" && string(rcpt.Actor) != actorFilter {
			continue
		}

		results = append(results, *rcpt)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CompletedAt.After(results[j].CompletedAt)
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

func (s *Store) readReceiptFile(filePath string) (*domain.Receipt, error) {
	fi, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("receipt: symlink detected for receipt file %s", filePath)
	}
	if fi.Size() > MaxReceiptFileSize {
		return nil, fmt.Errorf("receipt: receipt file %s exceeds maximum size limit (%d bytes)", filePath, fi.Size())
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var dto DTO
	dec := json.NewDecoder(io.LimitReader(file, MaxReceiptFileSize+1))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&dto); err != nil {
		return nil, fmt.Errorf("receipt: corrupt receipt record in %s: %w", filePath, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("receipt: trailing data in receipt file %s", filePath)
	}

	receipt, err := ConvertFromDTO(dto)
	if err != nil {
		return nil, fmt.Errorf("receipt: invalid receipt structure in %s: %w", filePath, err)
	}
	if err := receipt.Validate(); err != nil {
		return nil, fmt.Errorf("receipt: receipt validation failed in %s: %w", filePath, err)
	}

	return &receipt, nil
}

func (s *Store) readReceiptFileContext(ctx context.Context, filePath string) (*domain.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receipt, err := s.readReceiptFile(filePath)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return receipt, nil
}
