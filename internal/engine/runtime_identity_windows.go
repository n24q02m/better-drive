//go:build windows

package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openRuntimeHandle(path string) (*os.File, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("create file wrapper")
	}
	return file, nil
}

func probeRuntimeHandle(path string, file *os.File) (runtimeFileEvidence, error) {
	if file == nil {
		return runtimeFileEvidence{}, fmt.Errorf("%s handle is nil", path)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return runtimeFileEvidence{}, fmt.Errorf("file information: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return runtimeFileEvidence{}, fmt.Errorf("must not be a symlink or junction")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return runtimeFileEvidence{}, fmt.Errorf("must be a regular file")
	}
	identity := fmt.Sprintf("windows:volume=%08x;file=%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
	security, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return runtimeFileEvidence{}, fmt.Errorf("ACL readback: %w", err)
	}
	if security == nil || security.String() == "" {
		return runtimeFileEvidence{}, fmt.Errorf("ACL readback is unknown")
	}
	return runtimeFileEvidence{identity: identity, acl: security.String()}, nil
}

func sameRuntimePath(expected, actual string) bool {
	expected = strings.TrimPrefix(cleanRuntimePath(expected), `\\?\`)
	actual = strings.TrimPrefix(cleanRuntimePath(actual), `\\?\`)
	return strings.EqualFold(filepath.Clean(expected), filepath.Clean(actual))
}

func verifyRuntimeChildImage(cmd *exec.Cmd, expected *runtimeFile) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("child process is unavailable")
	}
	if expected == nil {
		return fmt.Errorf("enrolled executable evidence is unavailable")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open child for image readback: %w", err)
	}
	defer windows.CloseHandle(process)
	path, err := queryRuntimeProcessImage(process)
	if err != nil {
		return err
	}
	image, err := openRuntimeHandle(path)
	if err != nil {
		return fmt.Errorf("open child image for identity readback: %w", err)
	}
	defer image.Close()
	evidence, err := probeRuntimeHandle(path, image)
	if err != nil {
		return fmt.Errorf("child image evidence: %w", err)
	}
	if err := compareRuntimeChildImage(expected.path, expected.evidence.identity, runtimeChildImage{path: path, identity: evidence.identity}); err != nil {
		return err
	}
	return nil
}

func queryRuntimeProcessImage(process windows.Handle) (string, error) {
	const maxPath = 32768
	buffer := make([]uint16, maxPath)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", fmt.Errorf("query child image path: %w", err)
	}
	if size == 0 {
		return "", fmt.Errorf("query child image path returned empty path")
	}
	return windows.UTF16ToString(buffer[:size]), nil
}
