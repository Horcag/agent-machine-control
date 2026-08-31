package domain

import (
	"fmt"
	"unicode/utf8"
)

func validateOpenDimension(k string, v any) error {
	colsOrRows, err := toUint16(v)
	if err != nil {
		return fmt.Errorf("%w: invalid %s %v", ErrNonCanonicalParameter, k, v)
	}
	if k == "cols" && (colsOrRows < MinCols || colsOrRows > MaxCols) {
		return fmt.Errorf("%w: invalid cols %v", ErrNonCanonicalParameter, v)
	}
	if k == "rows" && (colsOrRows < MinRows || colsOrRows > MaxRows) {
		return fmt.Errorf("%w: invalid rows %v", ErrNonCanonicalParameter, v)
	}
	return nil
}

func validateOpenString(k string, v any) error {
	s, ok := v.(string)
	if !ok || len(s) == 0 {
		return fmt.Errorf("%w: invalid %s %v", ErrNonCanonicalParameter, k, v)
	}
	if k == "term" && ValidateTerminalType(s) != nil {
		return fmt.Errorf("%w: invalid term %v", ErrNonCanonicalParameter, v)
	}
	return nil
}

func validateOpenParam(k string, v any) error {
	switch k {
	case "cols", "rows":
		return validateOpenDimension(k, v)
	case "term":
		return validateOpenString(k, v)
	default:
		return fmt.Errorf("%w: unexpected parameter %q for session.open", ErrNonCanonicalParameter, k)
	}
}

func validateSessionOpenParams(params map[string]any) error {
	for k, v := range params {
		if err := validateOpenParam(k, v); err != nil {
			return err
		}
	}
	return nil
}

func validateRequiredSessionID(params map[string]any, kind string) error {
	if params == nil {
		return fmt.Errorf("%w: %s requires parameters", ErrNonCanonicalParameter, kind)
	}
	sessIDVal, ok := params["session_id"]
	if !ok {
		return fmt.Errorf("%w: %s requires session_id", ErrNonCanonicalParameter, kind)
	}
	sessID, ok := sessIDVal.(string)
	if !ok || ValidateSessionID(sessID) != nil {
		return fmt.Errorf("%w: invalid session_id for %s", ErrNonCanonicalParameter, kind)
	}
	return nil
}

func validateReadParam(k string, v any) error {
	switch k {
	case "session_id":
		return nil
	case "after_seq", "cursor":
		if err := validateNonNegativeUint64(v); err != nil {
			return fmt.Errorf("%w: invalid after_seq %v", ErrNonCanonicalParameter, v)
		}
	case "limit_bytes", "max_bytes":
		lim, err := toInt(v)
		if err != nil || lim <= 0 || lim > MaxSessionWriteBytes {
			return fmt.Errorf("%w: invalid limit_bytes %v", ErrNonCanonicalParameter, v)
		}
	case "timeout_ms":
		if t, err := toInt(v); err != nil || t < 0 {
			return fmt.Errorf("%w: invalid timeout_ms %v", ErrNonCanonicalParameter, v)
		}
	default:
		return fmt.Errorf("%w: unexpected parameter %q for session.read", ErrNonCanonicalParameter, k)
	}
	return nil
}

func validateSessionReadParams(params map[string]any) error {
	if err := validateRequiredSessionID(params, "session.read"); err != nil {
		return err
	}
	for k, v := range params {
		if err := validateReadParam(k, v); err != nil {
			return err
		}
	}
	return nil
}

func validateWriteDigestParams(params map[string]any, digestVal any) error {
	digestStr, isStr := digestVal.(string)
	if !isStr || (len(digestStr) != 64 && len(digestStr) != 71) {
		return fmt.Errorf("%w: invalid data_sha256", ErrNonCanonicalParameter)
	}
	lenVal, lenOk := params["data_length"]
	if !lenOk {
		return fmt.Errorf("%w: session.write with data_sha256 requires data_length", ErrNonCanonicalParameter)
	}
	dataLen, lErr := toInt(lenVal)
	if lErr != nil || dataLen <= 0 || dataLen > MaxSessionWriteBytes {
		return fmt.Errorf("%w: invalid data_length %v", ErrNonCanonicalParameter, lenVal)
	}
	for k := range params {
		if k != "session_id" && k != "data_sha256" && k != "data_length" {
			return fmt.Errorf("%w: unexpected parameter %q for session.write", ErrNonCanonicalParameter, k)
		}
	}
	return nil
}

func validateWritePlaintextParams(params map[string]any) error {
	dataVal, ok := params["data"]
	if !ok {
		dataVal, ok = params["text"]
	}
	if !ok {
		return fmt.Errorf("%w: session.write requires data parameter", ErrNonCanonicalParameter)
	}
	dataStr, ok := dataVal.(string)
	if !ok {
		return fmt.Errorf("%w: session.write data must be a string", ErrNonCanonicalParameter)
	}
	if len(dataStr) > MaxSessionWriteBytes {
		return fmt.Errorf("%w: session.write data exceeds maximum size (%d bytes)", ErrNonCanonicalParameter, MaxSessionWriteBytes)
	}
	if !utf8.ValidString(dataStr) {
		return fmt.Errorf("%w: session.write data contains invalid UTF-8 bytes", ErrNonCanonicalParameter)
	}
	for k := range params {
		if k != "session_id" && k != "data" && k != "text" {
			return fmt.Errorf("%w: unexpected parameter %q for session.write", ErrNonCanonicalParameter, k)
		}
	}
	return nil
}

func validateSessionWriteParams(params map[string]any) error {
	if err := validateRequiredSessionID(params, "session.write"); err != nil {
		return err
	}
	if digestVal, ok := params["data_sha256"]; ok {
		return validateWriteDigestParams(params, digestVal)
	}
	return validateWritePlaintextParams(params)
}

func validateSessionControlParams(params map[string]any) error {
	if err := validateRequiredSessionID(params, "session.control"); err != nil {
		return err
	}

	keyVal, ok := params["key"]
	if !ok {
		return fmt.Errorf("%w: session.control requires key parameter", ErrNonCanonicalParameter)
	}
	keyStr, ok := keyVal.(string)
	if !ok || ValidateControlKey(keyStr) != nil {
		return fmt.Errorf("%w: invalid control key %v for session.control", ErrNonCanonicalParameter, keyVal)
	}

	for k := range params {
		if k != "session_id" && k != "key" {
			return fmt.Errorf("%w: unexpected parameter %q for session.control", ErrNonCanonicalParameter, k)
		}
	}
	return nil
}

func validateWaitParam(k string, v any) error {
	switch k {
	case "session_id":
		return nil
	case "settle_ms":
		if s, err := toInt(v); err != nil || s < 0 {
			return fmt.Errorf("%w: invalid settle_ms %v", ErrNonCanonicalParameter, v)
		}
	case "regex", "pattern":
		s, ok := v.(string)
		if !ok || len(s) > MaxSessionRegexPatternLength {
			return fmt.Errorf("%w: invalid regex pattern length", ErrNonCanonicalParameter)
		}
	case "after_seq", "cursor":
		if err := validateNonNegativeUint64(v); err != nil {
			return fmt.Errorf("%w: invalid after_seq %v", ErrNonCanonicalParameter, v)
		}
	case "timeout_seconds":
		if t, err := toInt(v); err != nil || t < 0 {
			return fmt.Errorf("%w: invalid timeout_seconds %v", ErrNonCanonicalParameter, v)
		}
	default:
		return fmt.Errorf("%w: unexpected parameter %q for session.wait", ErrNonCanonicalParameter, k)
	}
	return nil
}

func validateSessionWaitParams(params map[string]any) error {
	if err := validateRequiredSessionID(params, "session.wait"); err != nil {
		return err
	}
	for k, v := range params {
		if err := validateWaitParam(k, v); err != nil {
			return err
		}
	}
	return nil
}

func validateSessionListParams(params map[string]any) error {
	for k, v := range params {
		switch k {
		case "machine", "machine_id":
			s, ok := v.(string)
			if !ok || (s != "" && ValidateMachineGUID(s) != nil) {
				return fmt.Errorf("%w: invalid machine GUID %v", ErrNonCanonicalParameter, v)
			}
		case "limit":
			if _, err := toInt(v); err != nil {
				return fmt.Errorf("%w: invalid limit %v", ErrNonCanonicalParameter, v)
			}
		default:
			return fmt.Errorf("%w: unexpected parameter %q for session.list", ErrNonCanonicalParameter, k)
		}
	}
	return nil
}

func validateSessionShowParams(params map[string]any) error {
	if err := validateRequiredSessionID(params, "session.show"); err != nil {
		return err
	}
	for k := range params {
		if k != "session_id" {
			return fmt.Errorf("%w: unexpected parameter %q for session.show", ErrNonCanonicalParameter, k)
		}
	}
	return nil
}

func validateSessionCloseParams(params map[string]any) error {
	if err := validateRequiredSessionID(params, "session.close"); err != nil {
		return err
	}

	for k := range params {
		if k != "session_id" {
			return fmt.Errorf("%w: unexpected parameter %q for session.close", ErrNonCanonicalParameter, k)
		}
	}
	return nil
}

func toUint16(v any) (uint16, error) {
	switch val := v.(type) {
	case int:
		if val < 0 || val > 65535 {
			return 0, fmt.Errorf("out of range: %d", val)
		}
		return uint16(val), nil
	case float64:
		if val < 0 || val > 65535 {
			return 0, fmt.Errorf("out of range: %v", val)
		}
		return uint16(val), nil
	case uint16:
		return val, nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}

func validateNonNegativeUint64(v any) error {
	switch val := v.(type) {
	case int:
		if val < 0 {
			return fmt.Errorf("negative: %d", val)
		}
		return nil
	case int64:
		if val < 0 {
			return fmt.Errorf("negative: %d", val)
		}
		return nil
	case float64:
		if val < 0 {
			return fmt.Errorf("negative: %v", val)
		}
		return nil
	case uint64:
		return nil
	default:
		return fmt.Errorf("unsupported type %T", v)
	}
}

func toInt(v any) (int, error) {
	switch val := v.(type) {
	case int:
		return val, nil
	case float64:
		return int(val), nil
	case int64:
		return int(val), nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
