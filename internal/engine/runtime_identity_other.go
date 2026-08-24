//go:build !windows && linux

package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func openRuntimeHandle(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("create file wrapper")
	}
	return file, nil
}

func probeRuntimeHandle(path string, file *os.File) (runtimeFileEvidence, error) {
	if file == nil {
		return runtimeFileEvidence{}, fmt.Errorf("%s handle is nil", path)
	}
	info, err := file.Stat()
	if err != nil {
		return runtimeFileEvidence{}, fmt.Errorf("file information: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return runtimeFileEvidence{}, fmt.Errorf("must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return runtimeFileEvidence{}, fmt.Errorf("must be a regular file")
	}
	identity, err := otherRuntimeFileIdentity(info)
	if err != nil {
		return runtimeFileEvidence{}, err
	}
	return runtimeFileEvidence{identity: identity, acl: fmt.Sprintf("mode:%04o", info.Mode().Perm())}, nil
}

func otherRuntimeFileIdentity(info os.FileInfo) (string, error) {
	system := info.Sys()
	if system == nil {
		return "", fmt.Errorf("file identity is unknown")
	}
	value := reflect.ValueOf(system)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", fmt.Errorf("file identity is unknown")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", fmt.Errorf("file identity is unknown")
	}
	dev, ok := runtimeNumericField(value.FieldByName("Dev"))
	if !ok {
		return "", fmt.Errorf("file device identity is unknown")
	}
	ino, ok := runtimeNumericField(value.FieldByName("Ino"))
	if !ok {
		return "", fmt.Errorf("file inode identity is unknown")
	}
	return fmt.Sprintf("linux:dev=%d;ino=%d", dev, ino), nil
}

func runtimeNumericField(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 {
			return 0, false
		}
		return uint64(value.Int()), true
	default:
		return 0, false
	}
}

func sameRuntimePath(expected, actual string) bool {
	return filepath.Clean(cleanRuntimePath(expected)) == filepath.Clean(cleanRuntimePath(actual))
}

func verifyRuntimeChildImage(cmd *exec.Cmd, expected *runtimeFile) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("child image verification unsupported on %s", runtime.GOOS)
	}
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("child process is unavailable")
	}
	if expected == nil {
		return fmt.Errorf("enrolled executable evidence is unavailable")
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("query child image path: %w", err)
	}
	path = strings.TrimSuffix(path, " (deleted)")
	image, err := openRuntimeHandle(path)
	if err != nil {
		return fmt.Errorf("open child image for identity readback: %w", err)
	}
	defer image.Close()
	evidence, err := probeRuntimeHandle(path, image)
	if err != nil {
		return fmt.Errorf("child image evidence: %w", err)
	}
	return compareRuntimeChildImage(expected.path, expected.evidence.identity, runtimeChildImage{path: path, identity: evidence.identity})
}
