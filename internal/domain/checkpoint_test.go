package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestCheckpointID_Validate(t *testing.T) {
	validID := domain.CheckpointID("a0b1c2d3-e4f5-6789-abcd-ef0123456789")
	if err := validID.Validate(); err != nil {
		t.Fatalf("expected valid checkpoint id, got %v", err)
	}
	if validID.String() != "a0b1c2d3-e4f5-6789-abcd-ef0123456789" {
		t.Errorf("unexpected string representation: %s", validID.String())
	}

	invalidIDs := []domain.CheckpointID{
		"",
		"short-id",
		domain.CheckpointID(strings.Repeat("a", 36)),
		"a0b1c2d3-e4f5-6789-abcd-ef012345678g", // non-hex
	}
	for _, id := range invalidIDs {
		if err := id.Validate(); err == nil {
			t.Errorf("expected error for invalid checkpoint id %q", id)
		}
	}
}

func TestCheckpointObservation_Validate(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	valid := domain.CheckpointObservation{
		ID:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Name:            "clean-baseline",
		VMID:            "b0b1c2d3-e4f5-6789-abcd-ef0123456789",
		ParentID:        "c0b1c2d3-e4f5-6789-abcd-ef0123456789",
		CheckpointType:  "Standard",
		CreatedAt:       now.Add(-1 * time.Hour),
		ObservedAt:      now,
		ObservationType: domain.ObservationObserved,
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid observation, got %v", err)
	}

	cloned := valid.Clone()
	if cloned.ID != valid.ID || cloned.Name != valid.Name {
		t.Errorf("cloned mismatch")
	}

	tests := []struct {
		name     string
		modify   func(c *domain.CheckpointObservation)
		expected error
	}{
		{"invalid ID", func(c *domain.CheckpointObservation) { c.ID = "invalid" }, domain.ErrInvalidCheckpointObservation},
		{"empty Name", func(c *domain.CheckpointObservation) { c.Name = "" }, domain.ErrInvalidCheckpointObservation},
		{"invalid VMID", func(c *domain.CheckpointObservation) { c.VMID = "invalid" }, domain.ErrInvalidCheckpointObservation},
		{"invalid ParentID", func(c *domain.CheckpointObservation) { c.ParentID = "not-a-guid" }, domain.ErrInvalidCheckpointObservation},
		{"long CheckpointType", func(c *domain.CheckpointObservation) { c.CheckpointType = strings.Repeat("x", 200) }, domain.ErrInvalidCheckpointObservation},
		{"zero CreatedAt", func(c *domain.CheckpointObservation) { c.CreatedAt = time.Time{} }, domain.ErrInvalidCheckpointObservation},
		{"zero ObservedAt", func(c *domain.CheckpointObservation) { c.ObservedAt = time.Time{} }, domain.ErrInvalidCheckpointObservation},
		{"invalid ObservationType", func(c *domain.CheckpointObservation) { c.ObservationType = "inferred" }, domain.ErrInvalidObservationType},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.modify(&c)
			err := c.Validate()
			if err == nil || !errors.Is(err, tc.expected) {
				t.Errorf("expected error %v, got %v", tc.expected, err)
			}
		})
	}
}
