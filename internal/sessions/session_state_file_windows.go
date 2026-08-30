//go:build windows

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func openSessionStateFile(sessionsDir, filename string) (*os.File, error) {
	return openSessionStateFileContext(context.Background(), sessionsDir, filename)
}

func openSessionStateFileContext(ctx context.Context, sessionsDir, filename string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(sessionsDir, filename))
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	var handle windows.Handle
	for {
		handle, err = windows.CreateFile(
			path,
			windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err == nil || !retryableSessionStateOpenError(err) || !time.Now().Before(deadline) {
			break
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("sessions: canonical session is not a regular non-reparse file")
	}
	file := os.NewFile(uintptr(handle), filename)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("sessions: failed to adopt canonical session handle")
	}
	return file, nil
}

func verifySessionFilePublication(ctx context.Context, filePath string, expected []byte) error {
	file, err := openSessionStateFileContext(ctx, filepath.Dir(filePath), filepath.Base(filePath))
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSessionStateBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(data) > maxSessionStateBytes {
		return fmt.Errorf("sessions: published canonical session exceeds %d bytes", maxSessionStateBytes)
	}
	if !bytes.Equal(data, expected) {
		return errors.New("sessions: published canonical session payload does not match replacement")
	}
	return nil
}

func retryableSessionStateOpenError(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_DELETE_PENDING)
}
