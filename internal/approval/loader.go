package approval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const (
	// MaxApprovalFileSize is 64 KB.
	MaxApprovalFileSize = 64 * 1024
)

// LoadFromFile securely reads and validates an Approval from a file path.
func LoadFromFile(path string) (*domain.Approval, error) {
	if path == "" {
		return nil, domain.ErrInvalidApprovalRecord
	}

	cleanPath := filepath.Clean(path)
	fi, err := os.Lstat(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("approval: cannot open approval file %q: %w", path, err)
	}

	// Reject symlinks
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlinkNotAllowed
	}

	// Reject if too large
	if fi.Size() > MaxApprovalFileSize {
		return nil, ErrFileTooLarge
	}

	if err := validateApprovalFilePrivacy(cleanPath, fi); err != nil {
		return nil, err
	}

	file, err := os.Open(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("approval: failed to read file: %w", err)
	}
	defer file.Close()

	var dto DTO
	dec := json.NewDecoder(io.LimitReader(file, MaxApprovalFileSize+1))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&dto); err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrInvalidApprovalRecord, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, ErrTrailingData
	}

	approval, err := ConvertFromDTO(dto)
	if err != nil {
		return nil, err
	}

	if err := approval.Validate(); err != nil {
		return nil, err
	}

	return &approval, nil
}
