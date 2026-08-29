package domain

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DeepCloneMap creates a deep copy of a parameter map.
func DeepCloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	clone := make(map[string]any, len(m))
	for k, v := range m {
		clone[k] = DeepCloneValue(v)
	}
	return clone
}

// DeepCloneValue creates a deep copy of any canonical parameter value.
func DeepCloneValue(val any) any {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case map[string]any:
		return DeepCloneMap(v)
	case map[string]string:
		m := make(map[string]string, len(v))
		maps.Copy(m, v)
		return m
	case []any:
		s := make([]any, len(v))
		for i, elem := range v {
			s[i] = DeepCloneValue(elem)
		}
		return s
	case []string:
		return slices.Clone(v)
	default:
		return v
	}
}

// CanonicalizeParameters encodes parameter map into deterministic canonical bytes.
func CanonicalizeParameters(params map[string]any) ([]byte, error) {
	if params == nil {
		return []byte("M0:{}"), nil
	}
	var buf bytes.Buffer
	if err := encodeCanonicalValue(&buf, params); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeCanonicalValue(buf *bytes.Buffer, val any) error {
	if val == nil {
		return fmt.Errorf("%w: nil values are not allowed in canonical parameters", ErrNonCanonicalParameter)
	}

	switch v := val.(type) {
	case string:
		return encodeString(buf, v)
	case bool:
		return encodeBool(buf, v)
	case int, int8, int16, int32, int64:
		return encodeSignedInt(buf, v)
	case uint, uint8, uint16, uint32, uint64:
		return encodeUnsignedInt(buf, v)
	case []any:
		return encodeSlice(buf, v)
	case []string:
		return encodeStringSlice(buf, v)
	case map[string]any:
		return encodeMap(buf, v)
	case map[string]string:
		return encodeStringMap(buf, v)
	default:
		return fmt.Errorf("%w: unsupported parameter type %T", ErrNonCanonicalParameter, val)
	}
}

func encodeString(buf *bytes.Buffer, v string) error {
	if !utf8.ValidString(v) {
		return fmt.Errorf("%w: string contains invalid UTF-8 bytes", ErrNonCanonicalParameter)
	}
	if strings.ContainsRune(v, 0) {
		return fmt.Errorf("%w: string contains null byte", ErrNonCanonicalParameter)
	}
	buf.WriteString("S")
	buf.WriteString(strconv.Itoa(len(v)))
	buf.WriteString(":")
	buf.WriteString(v)
	return nil
}

func encodeBool(buf *bytes.Buffer, v bool) error {
	if v {
		buf.WriteString("B:true")
	} else {
		buf.WriteString("B:false")
	}
	return nil
}

func encodeSignedInt(buf *bytes.Buffer, val any) error {
	var num int64
	switch v := val.(type) {
	case int:
		num = int64(v)
	case int8:
		num = int64(v)
	case int16:
		num = int64(v)
	case int32:
		num = int64(v)
	case int64:
		num = v
	}
	buf.WriteString("I:")
	buf.WriteString(strconv.FormatInt(num, 10))
	return nil
}

func encodeUnsignedInt(buf *bytes.Buffer, val any) error {
	var num uint64
	switch v := val.(type) {
	case uint:
		num = uint64(v)
	case uint8:
		num = uint64(v)
	case uint16:
		num = uint64(v)
	case uint32:
		num = uint64(v)
	case uint64:
		num = v
	}
	buf.WriteString("U:")
	buf.WriteString(strconv.FormatUint(num, 10))
	return nil
}

func encodeSlice(buf *bytes.Buffer, v []any) error {
	buf.WriteString("L")
	buf.WriteString(strconv.Itoa(len(v)))
	buf.WriteString(":[")
	for _, elem := range v {
		if err := encodeCanonicalValue(buf, elem); err != nil {
			return err
		}
	}
	buf.WriteString("]")
	return nil
}

func encodeStringSlice(buf *bytes.Buffer, v []string) error {
	buf.WriteString("L")
	buf.WriteString(strconv.Itoa(len(v)))
	buf.WriteString(":[")
	for _, elem := range v {
		if err := encodeString(buf, elem); err != nil {
			return err
		}
	}
	buf.WriteString("]")
	return nil
}

func encodeMap(buf *bytes.Buffer, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		if !utf8.ValidString(k) || strings.ContainsRune(k, 0) {
			return fmt.Errorf("%w: map key contains invalid UTF-8 or null byte", ErrNonCanonicalParameter)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf.WriteString("M")
	buf.WriteString(strconv.Itoa(len(keys)))
	buf.WriteString(":{")
	for _, k := range keys {
		if err := encodeString(buf, k); err != nil {
			return err
		}
		if err := encodeCanonicalValue(buf, m[k]); err != nil {
			return err
		}
	}
	buf.WriteString("}")
	return nil
}

func encodeStringMap(buf *bytes.Buffer, m map[string]string) error {
	converted := make(map[string]any, len(m))
	for k, v := range m {
		converted[k] = v
	}
	return encodeMap(buf, converted)
}
