package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func decodeStrictJSONObject(r io.Reader, dst any) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return io.ErrUnexpectedEOF
	}
	if err := rejectDuplicateTopLevelFields(data); err != nil {
		return err
	}
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(dst); err != nil {
		return err
	}
	return requireJSONEOF(strict)
}

func rejectDuplicateTopLevelFields(data []byte) error {
	keys := make(map[string]struct{})
	scan := json.NewDecoder(bytes.NewReader(data))
	opening, err := scan.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return errors.New("request body must be a JSON object")
	}
	for scan.More() {
		token, err := scan.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("request object contains a non-string key")
		}
		if _, exists := keys[key]; exists {
			return fmt.Errorf("duplicate top-level field %q", key)
		}
		keys[key] = struct{}{}
		var value json.RawMessage
		if err := scan.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := scan.Token(); err != nil {
		return err
	}
	return requireJSONEOF(scan)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing data in request body")
		}
		return err
	}
	return nil
}
