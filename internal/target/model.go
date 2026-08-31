// Package target persists the single canonical local machine selected as AMC's default target.
package target

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const (
	SchemaVersion     = 1
	StateFileName     = "default.json"
	MaxDocumentBytes  = 4096
	MaxAliases        = 16
	reservedReference = "default"
)

var (
	ErrNoDefault             = errors.New("target: no default target is enrolled")
	ErrInvalidDocument       = errors.New("target: invalid default target document")
	ErrInsecureState         = errors.New("target: protected state security validation failed")
	ErrDurabilityPending     = errors.New("target: committed state requires an exact durability repair")
	ErrCommittedNotDurable   = errors.New("target: state committed but durability is not confirmed")
	ErrUnsupportedHost       = errors.New("target: only the local host can be enrolled")
	ErrDifferentTarget       = errors.New("target: reference does not identify the enrolled target")
	ErrHostSecurityUnproven  = errors.New("target: Windows host-path security could not be proven")
	ErrAtomicCommitUncertain = errors.New("target: atomic replacement returned without effect truth")
	ErrInventoryRefresh      = errors.New("target: local inventory refresh failed")
	ErrAccessDenied          = errors.New("target: operator target authority is required")
	ErrApprovalRequired      = errors.New("target: exact active approval is required")
)

// Default is the complete persisted target authority. Display names are deliberately absent.
type Default struct {
	Locator domain.MachineLocator
	Aliases []string
}

// NewDefault validates and canonicalizes the persisted target authority.
func NewDefault(locator domain.MachineLocator, aliases []string) (Default, error) {
	if err := locator.Validate(); err != nil {
		return Default{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if locator.HostID != domain.LocalHostID {
		return Default{}, ErrUnsupportedHost
	}
	if len(aliases) > MaxAliases {
		return Default{}, fmt.Errorf("%w: aliases exceed %d entries", ErrInvalidDocument, MaxAliases)
	}

	canonical := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		normalized, err := domain.NormalizeExactAlias(alias)
		if err != nil || normalized != alias {
			return Default{}, fmt.Errorf("%w: alias %q is not canonical", ErrInvalidDocument, alias)
		}
		if alias == reservedReference || alias == locator.String() || alias == locator.VMID {
			return Default{}, fmt.Errorf("%w: alias %q collides with a reserved reference", ErrInvalidDocument, alias)
		}
		if _, duplicate := seen[alias]; duplicate {
			return Default{}, fmt.Errorf("%w: duplicate alias %q", ErrInvalidDocument, alias)
		}
		seen[alias] = struct{}{}
		canonical = append(canonical, alias)
	}
	sort.Strings(canonical)
	return Default{Locator: locator, Aliases: canonical}, nil
}

// Clone returns an independent copy of the default authority.
func (d Default) Clone() Default {
	d.Aliases = slices.Clone(d.Aliases)
	return d
}

func (d Default) equal(other Default) bool {
	return d.Locator == other.Locator && slices.Equal(d.Aliases, other.Aliases)
}

// StateDigest returns a redacted exact digest of canonical target authority.
func StateDigest(value *Default) string {
	hash := sha256.New()
	if value == nil {
		hash.Write([]byte("absent\x00"))
		return hex.EncodeToString(hash.Sum(nil))
	}
	hash.Write([]byte(value.Locator.String()))
	hash.Write([]byte{0})
	for _, alias := range value.Aliases {
		digest := sha256.Sum256([]byte(alias))
		hash.Write(digest[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// TransitionDigest binds prior and desired authority without exposing alias plaintext.
func TransitionDigest(prior, desired *Default) string {
	digest := sha256.Sum256([]byte(StateDigest(prior) + "\x00" + StateDigest(desired)))
	return hex.EncodeToString(digest[:])
}

type document struct {
	SchemaVersion  int      `json:"schema_version"`
	DefaultLocator string   `json:"default_locator"`
	Aliases        []string `json:"aliases"`
}

func encode(value Default) ([]byte, error) {
	canonical, err := NewDefault(value.Locator, value.Aliases)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(document{
		SchemaVersion:  SchemaVersion,
		DefaultLocator: canonical.Locator.String(),
		Aliases:        canonical.Aliases,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %v", ErrInvalidDocument, err)
	}
	payload = append(payload, '\n')
	if len(payload) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: encoded document exceeds %d bytes", ErrInvalidDocument, MaxDocumentBytes)
	}
	return payload, nil
}

func decode(payload []byte) (Default, error) {
	if len(payload) == 0 || len(payload) > MaxDocumentBytes {
		return Default{}, fmt.Errorf("%w: document size must be 1-%d bytes", ErrInvalidDocument, MaxDocumentBytes)
	}
	if err := rejectDuplicateDocumentFields(payload); err != nil {
		return Default{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var stored document
	if err := decoder.Decode(&stored); err != nil {
		return Default{}, fmt.Errorf("%w: decode: %v", ErrInvalidDocument, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		return Default{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if stored.SchemaVersion != SchemaVersion {
		return Default{}, fmt.Errorf("%w: unsupported schema version %d", ErrInvalidDocument, stored.SchemaVersion)
	}
	if stored.Aliases == nil {
		return Default{}, fmt.Errorf("%w: aliases must be a JSON array", ErrInvalidDocument)
	}
	locator, err := domain.ParseMachineLocator(stored.DefaultLocator)
	if err != nil || locator.String() != stored.DefaultLocator {
		return Default{}, fmt.Errorf("%w: default locator is not canonical", ErrInvalidDocument)
	}
	return NewDefault(locator, stored.Aliases)
}

func rejectDuplicateDocumentFields(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("%w: expected JSON object", ErrInvalidDocument)
	}
	seen := make(map[string]struct{}, 3)
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%w: read field: %v", ErrInvalidDocument, err)
		}
		name, ok := field.(string)
		if !ok {
			return fmt.Errorf("%w: object field name is not a string", ErrInvalidDocument)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate field %q", ErrInvalidDocument, name)
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("%w: decode field %q: %v", ErrInvalidDocument, name, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return fmt.Errorf("%w: unterminated JSON object", ErrInvalidDocument)
	}
	return nil
}
