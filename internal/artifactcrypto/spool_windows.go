//go:build windows

package artifactcrypto

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsSpoolCreateAttempts = 8

func createSecureSpool() (*os.File, error) {
	sid, err := currentProcessSID()
	if err != nil {
		return nil, err
	}
	sidText := sid.String()
	if sidText == "" {
		return nil, errors.New("current process SID is unavailable")
	}
	securityDescriptor, err := windows.SecurityDescriptorFromString("O:" + sidText + "G:" + sidText + "D:P(A;;FA;;;" + sidText + ")")
	if err != nil {
		return nil, err
	}
	securityAttributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
	}
	for range windowsSpoolCreateAttempts {
		path, err := randomSpoolPath()
		if err != nil {
			return nil, err
		}
		pathPtr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		handle, err := windows.CreateFile(
			pathPtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.DELETE,
			0,
			&securityAttributes,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE,
			0,
		)
		if err != nil {
			if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				continue
			}
			return nil, err
		}
		file := os.NewFile(uintptr(handle), path)
		if file == nil {
			_ = windows.CloseHandle(handle)
			return nil, errors.New("create artifact spool handle failed")
		}
		if err := verifyWindowsSpool(file, sid); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
	return nil, errors.New("create artifact spool name collision")
}

func cleanupSecureSpool(spool *os.File) error {
	if spool == nil {
		return nil
	}
	var cleanupErrs []error
	name := spool.Name()
	if err := spool.Close(); err != nil {
		cleanupErrs = append(cleanupErrs, wrapError("close artifact spool", err))
	}
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErrs = append(cleanupErrs, wrapError("remove artifact spool", err))
	}
	return errors.Join(cleanupErrs...)
}

func currentProcessSID() (*windows.SID, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	if tokenUser == nil || tokenUser.User.Sid == nil {
		return nil, errors.New("current process SID is unavailable")
	}
	return tokenUser.User.Sid, nil
}

func randomSpoolPath() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	return filepath.Join(os.TempDir(), "better-drive-artifact-"+hex.EncodeToString(randomBytes[:])+".tmp"), nil
}

func verifyWindowsSpool(file *os.File, expectedSID *windows.SID) error {
	if expectedSID == nil || !expectedSID.IsValid() {
		return errors.New("expected process SID is invalid")
	}
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &fileInfo); err != nil {
		return err
	}
	if fileInfo.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return errors.New("artifact spool is not a private regular file")
	}
	securityDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if securityDescriptor == nil || !securityDescriptor.IsValid() {
		return errors.New("artifact spool security descriptor is invalid")
	}
	owner, _, err := securityDescriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() || !owner.Equals(expectedSID) {
		return errors.New("artifact spool owner is invalid")
	}
	control, _, err := securityDescriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("artifact spool DACL is not protected")
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("artifact spool DACL is invalid")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil {
		return errors.New("artifact spool DACL entry is invalid")
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return errors.New("artifact spool DACL ACE type is invalid")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || !aceSID.Equals(expectedSID) {
		return errors.New("artifact spool DACL trustee is invalid")
	}
	if ace.Mask&windows.ACCESS_MASK(windows.STANDARD_RIGHTS_ALL|windows.SPECIFIC_RIGHTS_ALL|windows.GENERIC_ALL|windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE) == 0 {
		return errors.New("artifact spool DACL permissions are invalid")
	}
	return nil
}
