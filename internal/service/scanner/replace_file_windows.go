//go:build windows

package scanner

import (
	"errors"
	"os"
	"syscall"
	"time"
)

const (
	windowsSharingViolation = syscall.Errno(32)
	windowsLockViolation    = syscall.Errno(33)
)

func replaceFile(tmpPath, path string) error {
	var err error
	for attempt := 0; attempt < 40; attempt++ {
		err = os.Rename(tmpPath, path)
		if err == nil {
			return nil
		}
		if !isTransientReplaceError(err) {
			return err
		}
		time.Sleep(replaceRetryDelay(attempt))
	}
	return err
}

func isTransientReplaceError(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windowsSharingViolation) ||
		errors.Is(err, windowsLockViolation)
}

func replaceRetryDelay(attempt int) time.Duration {
	delay := time.Duration(10+attempt*5) * time.Millisecond
	if delay > 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	return delay
}
