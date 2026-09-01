//go:build windows

package cli

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func atomicReplaceFile(source, destination string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	const attempts = 10
	for attempt := range attempts {
		err = windows.MoveFileEx(
			sourcePath,
			destinationPath,
			windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
		)
		if err == nil {
			return nil
		}
		if attempt == attempts-1 ||
			(!errors.Is(err, windows.ERROR_ACCESS_DENIED) &&
				!errors.Is(err, windows.ERROR_SHARING_VIOLATION)) {
			return err
		}
		time.Sleep(5 * time.Millisecond << attempt)
	}
	return err
}
