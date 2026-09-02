//go:build windows

package protectedfs

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFileAllAccess = windows.ACCESS_MASK(0x001F01FF)

func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sid, err := currentProcessSID()
	if err != nil {
		return err
	}
	attributes, err := privateSecurityAttributes(sid, true)
	if err != nil {
		return err
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(pathPointer, attributes); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	return VerifyPrivateDir(path)
}

func VerifyPrivateDir(path string) error {
	sid, err := currentProcessSID()
	if err != nil {
		return err
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return verifyPrivateHandle(handle, sid, true)
}

func CreatePrivateFile(path string) (*os.File, error) {
	sid, err := currentProcessSID()
	if err != nil {
		return nil, err
	}
	attributes, err := privateSecurityAttributes(sid, false)
	if err != nil {
		return nil, err
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.DELETE,
		0,
		attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("create protected file handle failed")
	}
	if err := verifyPrivateHandle(handle, sid, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func OpenPrivateFile(path string) (*os.File, error) {
	sid, err := currentProcessSID()
	if err != nil {
		return nil, err
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("open protected file handle failed")
	}
	if err := verifyPrivateHandle(handle, sid, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func VerifyPrivateFile(file *os.File) error {
	if file == nil {
		return errors.New("protected file is required")
	}
	sid, err := currentProcessSID()
	if err != nil {
		return err
	}
	return verifyPrivateHandle(windows.Handle(file.Fd()), sid, false)
}

func currentProcessSID() (*windows.SID, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	if tokenUser == nil || tokenUser.User.Sid == nil || !tokenUser.User.Sid.IsValid() {
		return nil, errors.New("current process SID is unavailable")
	}
	return tokenUser.User.Sid, nil
}

func privateSecurityAttributes(sid *windows.SID, directory bool) (*windows.SecurityAttributes, error) {
	if sid == nil || !sid.IsValid() || sid.String() == "" {
		return nil, errors.New("current process SID is unavailable")
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + sid.String() + "G:" + sid.String() + "D:P(A;" + inheritance + ";FA;;;" + sid.String() + ")",
	)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, nil
}

func verifyPrivateHandle(handle windows.Handle, expectedSID *windows.SID, directory bool) error {
	if handle == windows.InvalidHandle || expectedSID == nil || !expectedSID.IsValid() {
		return errors.New("protected handle or owner SID is invalid")
	}
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fileInfo); err != nil {
		return err
	}
	if fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("protected path must not be a reparse point")
	}
	isDirectory := fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory {
		return errors.New("protected path type is invalid")
	}
	securityDescriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if securityDescriptor == nil || !securityDescriptor.IsValid() {
		return errors.New("protected path security descriptor is invalid")
	}
	owner, _, err := securityDescriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() || !owner.Equals(expectedSID) {
		return errors.New("protected path owner is not the current process owner")
	}
	control, _, err := securityDescriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("protected path DACL is not protected")
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("protected path DACL must contain exactly one owner ACE")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return errors.New("protected path DACL ACE is invalid")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || !aceSID.Equals(expectedSID) {
		return errors.New("protected path DACL trustee is not the current process owner")
	}
	if ace.Mask&windowsFileAllAccess != windowsFileAllAccess {
		return errors.New("protected path owner ACE lacks full access")
	}
	if directory {
		requiredFlags := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
		if ace.Header.AceFlags&requiredFlags != requiredFlags {
			return errors.New("protected directory DACL does not propagate owner-only access")
		}
	}
	return nil
}
