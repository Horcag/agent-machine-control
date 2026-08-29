package operations

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const (
	// MaxOperationFileSize bounds the size of an operation record file (64 KB).
	MaxOperationFileSize = 64 * 1024
)

// SaveRecord persists an OperationRecord atomically to disk with 0600 permissions.
func SaveRecord(dir string, rec domain.OperationRecord) error {
	if dir == "" {
		return nil
	}
	if err := rec.Validate(); err != nil {
		return fmt.Errorf("operations: invalid record: %w", err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("operations: marshal error: %w", err)
	}

	finalPath := filepath.Join(dir, fmt.Sprintf("%s.json", rec.ID))
	tmpPath := filepath.Join(dir, fmt.Sprintf("%s.tmp.%d", rec.ID, time.Now().UnixNano()))

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("operations: failed to create temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("operations: failed to rename temp file: %w", err)
	}

	return statedir.SyncDir(dir)
}

// ReadRecord reads and validates an OperationRecord from disk.
func ReadRecord(dir, opID string) (*domain.OperationRecord, error) {
	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, err
	}

	filePath := filepath.Join(dir, fmt.Sprintf("%s.json", opID))
	fi, err := os.Lstat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrOperationNotFound
		}
		return nil, fmt.Errorf("operations: failed to stat record %s: %w", filePath, err)
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("operations: symlink detected for record %s", filePath)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("operations: record %s is not a regular file", filePath)
	}
	if fi.Size() > MaxOperationFileSize {
		return nil, fmt.Errorf("operations: record %s exceeds max size limit (%d bytes)", filePath, fi.Size())
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var rec domain.OperationRecord
	dec := json.NewDecoder(io.LimitReader(file, MaxOperationFileSize+1))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("operations: corrupt record in %s: %w", filePath, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("operations: trailing data in record file %s", filePath)
	}

	if err := rec.Validate(); err != nil {
		return nil, fmt.Errorf("operations: record validation failed in %s: %w", filePath, err)
	}

	return &rec, nil
}

// ListRecords scans and filters operation records from the operations directory.
func ListRecords(dir string, opts ListOptions) ([]domain.OperationRecord, error) {
	limit := clampLimit(opts.Limit, 50, 1000)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.OperationRecord{}, nil
		}
		return nil, fmt.Errorf("operations: failed to read operations dir: %w", err)
	}

	var records []domain.OperationRecord
	for _, entry := range entries {
		if entry.IsDir() || !isOperationRecordFile(entry.Name()) {
			continue
		}

		opID := strings.TrimSuffix(entry.Name(), ".json")
		if err := domain.ValidateOperationID(opID); err != nil {
			return nil, fmt.Errorf("operations: invalid operation record filename %s: %w", entry.Name(), err)
		}

		rec, err := ReadRecord(dir, opID)
		if err != nil {
			return nil, fmt.Errorf("operations: failed to read operation record %s: %w", opID, err)
		}

		if matchesListOptions(rec, opts) {
			records = append(records, *rec)
		}
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})

	if len(records) > limit {
		records = records[:limit]
	}

	return records, nil
}

func isOperationRecordFile(name string) bool {
	return strings.HasSuffix(name, ".json") &&
		!strings.Contains(name, ".tmp.") &&
		!strings.Contains(name, ".events.")
}

func matchesListOptions(rec *domain.OperationRecord, opts ListOptions) bool {
	if opts.State != "" && rec.State != opts.State {
		return false
	}
	if opts.Machine != "" && rec.Target != opts.Machine {
		return false
	}
	return true
}

func clampLimit(val, def, maxVal int) int {
	if val <= 0 {
		return def
	}
	if val > maxVal {
		return maxVal
	}
	return val
}
