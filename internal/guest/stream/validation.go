package stream

import (
	"time"
)

func validate(req Request) error {
	if req.ID == "" || len(req.ID) > 128 || req.DeadlineUTC.IsZero() || !req.DeadlineUTC.After(time.Now()) || req.Size < 0 || req.Size > MaxTransferSize || req.MaxOutput < 0 || req.MaxOutput > MaxTransferSize {
		return ErrRequest
	}
	return validateOperation(req)
}
func validateOperation(req Request) error {
	switch req.Operation {
	case "put", "exec":
		return validateInput(req)
	case "observe", "get", "remove-owned":
		return validateReferences(req)
	default:
		return ErrRequest
	}
}
func validateInput(req Request) error {
	if !validDigest(req.SHA256) {
		return ErrRequest
	}
	if req.Operation == "put" && req.ExpectedRoot == nil {
		return ErrRequest
	}
	if req.Operation == "exec" && (req.Size > MaxScriptSize || req.MaxOutput == 0 || req.MaxOutput > MaxCommandOutput) {
		return ErrRequest
	}
	return nil
}
func validateReferences(req Request) error {
	if req.Size != 0 {
		return ErrRequest
	}
	switch req.Operation {
	case "observe":
		if req.MaxOutput != 0 || req.Path == "" {
			return ErrRequest
		}
	case "get":
		if req.MaxOutput == 0 || req.Path == "" || len(req.Owned) != 1 || req.Owned[0].Path != req.Path {
			return ErrRequest
		}
	case "remove-owned":
		if len(req.Owned) == 0 || len(req.Owned) > MaxManifest {
			return ErrRequest
		}
	}
	return nil
}
