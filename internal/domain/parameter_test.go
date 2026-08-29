package domain_test

import (
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestCanonicalizeParameters_AllTypesAndRejection(t *testing.T) {
	tests := []struct {
		name    string
		params  map[string]any
		wantErr bool
	}{
		{
			name:    "nil parameter map allowed (empty)",
			params:  nil,
			wantErr: false,
		},
		{
			name: "all integer and map types valid",
			params: map[string]any{
				"i":         int(1),
				"i8":        int8(8),
				"i16":       int16(16),
				"i32":       int32(32),
				"i64":       int64(64),
				"u":         uint(2),
				"u8":        uint8(8),
				"u16":       uint16(16),
				"u32":       uint32(32),
				"u64":       uint64(64),
				"b_t":       true,
				"b_f":       false,
				"str_slice": []string{"a", "b"},
				"map_str": map[string]string{
					"k1": "v1",
					"k2": "v2",
				},
			},
			wantErr: false,
		},
		{
			name:    "float rejected",
			params:  map[string]any{"val": 3.14159},
			wantErr: true,
		},
		{
			name:    "nil value in map rejected",
			params:  map[string]any{"val": nil},
			wantErr: true,
		},
		{
			name:    "invalid UTF-8 string rejected",
			params:  map[string]any{"val": "invalid \xff\xfe string"},
			wantErr: true,
		},
		{
			name:    "null byte in string rejected",
			params:  map[string]any{"val": "has\x00null"},
			wantErr: true,
		},
		{
			name:    "null byte in map key rejected",
			params:  map[string]any{"k\x00ey": "val"},
			wantErr: true,
		},
		{
			name: "null byte in string map key rejected",
			params: map[string]any{
				"bad_map": map[string]string{"k\x00ey": "val"},
			},
			wantErr: true,
		},
		{
			name: "invalid utf8 in string map key rejected",
			params: map[string]any{
				"bad_map": map[string]string{"\xff\xfe": "val"},
			},
			wantErr: true,
		},
		{
			name:    "function value rejected",
			params:  map[string]any{"fn": func() {}},
			wantErr: true,
		},
		{
			name:    "channel value rejected",
			params:  map[string]any{"ch": make(chan int)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.CanonicalizeParameters(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("CanonicalizeParameters() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, domain.ErrNonCanonicalParameter) {
				t.Errorf("expected ErrNonCanonicalParameter, got %v", err)
			}
		})
	}
}

func TestDeepCloneMap_NilAndNested(t *testing.T) {
	if domain.DeepCloneMap(nil) != nil {
		t.Errorf("DeepCloneMap(nil) expected nil")
	}
	if domain.DeepCloneValue(nil) != nil {
		t.Errorf("DeepCloneValue(nil) expected nil")
	}

	original := map[string]any{
		"nested": map[string]any{
			"key": "val",
		},
		"str_map": map[string]string{
			"k": "v",
		},
		"slice":     []any{"item", 123},
		"str_slice": []string{"a", "b"},
		"primitive": 42,
	}

	cloned := domain.DeepCloneMap(original)
	if cloned == nil {
		t.Fatalf("DeepCloneMap returned nil")
	}

	// Mutate clone
	cloned["nested"].(map[string]any)["key"] = "changed"
	cloned["str_map"].(map[string]string)["k"] = "changed"
	cloned["slice"].([]any)[0] = "changed"
	cloned["str_slice"].([]string)[0] = "changed"

	// Verify original untouched
	if original["nested"].(map[string]any)["key"] != "val" {
		t.Errorf("original nested map was mutated")
	}
	if original["str_map"].(map[string]string)["k"] != "v" {
		t.Errorf("original string map was mutated")
	}
	if original["slice"].([]any)[0] != "item" {
		t.Errorf("original slice was mutated")
	}
	if original["str_slice"].([]string)[0] != "a" {
		t.Errorf("original string slice was mutated")
	}
}
