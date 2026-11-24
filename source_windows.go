package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const deleteAccess = 0x00010000
const fileDispositionInfo = 4

var setFileInformationByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("SetFileInformationByHandle")

func windowsSourcePath(path string) (*uint16, error) {
	nativePath := path
	if len(path) >= 248 && !strings.HasPrefix(path, `\\?\`) {
		if strings.HasPrefix(path, `\\`) {
			nativePath = `\\?\UNC\` + path[2:]
		} else {
			nativePath = `\\?\` + path
		}
	}
	return syscall.UTF16PtrFromString(nativePath)
}

func openSource(path string, deleting bool) (*os.File, error) {
	name, err := windowsSourcePath(path)
	if err != nil {
		return nil, err
	}
	access := uint32(syscall.GENERIC_READ)
	if deleting {
		access |= deleteAccess
	}
	// Deny other handles write and delete access until verification finishes.
	handle, err := syscall.CreateFile(name, access, syscall.FILE_SHARE_READ, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info syscall.ByHandleFileInformation
	err = syscall.GetFileInformationByHandle(handle, &info)
	if err != nil || info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		syscall.CloseHandle(handle)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("filen er et link eller har et reparse point")
	}
	return os.NewFile(uintptr(handle), path), nil
}

func sourceStillAtPath(file *os.File, _ os.FileInfo) (bool, error) {
	name, err := windowsSourcePath(file.Name())
	if err != nil {
		return false, err
	}
	// Request metadata only and allow the access already held by the source handle.
	const share = syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE
	pathHandle, err := syscall.CreateFile(name, 0, share, nil, syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(pathHandle)
	var actual, expected syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(pathHandle, &actual); err != nil {
		return false, err
	}
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &expected); err != nil {
		return false, err
	}
	return actual.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0 &&
		actual.VolumeSerialNumber == expected.VolumeSerialNumber &&
		actual.FileIndexHigh == expected.FileIndexHigh && actual.FileIndexLow == expected.FileIndexLow, nil
}

func removeSource(file *os.File) error {
	// Delete by handle so a replacement pathname is never removed.
	deleteOnClose := byte(1)
	success, _, err := setFileInformationByHandle.Call(
		file.Fd(), fileDispositionInfo, uintptr(unsafe.Pointer(&deleteOnClose)), unsafe.Sizeof(deleteOnClose))
	if success == 0 {
		return err
	}
	return file.Close()
}

func archiveRunLock(appDir string) (func(), error) {
	path, err := syscall.UTF16PtrFromString(filepath.Join(appDir, archiveRunLockName))
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, syscall.Errno(32)) {
		return nil, errors.New("En arkivkørsel er allerede aktiv. Stop den, før du starter en ny.")
	}
	if err != nil {
		return nil, fmt.Errorf("åbn lås for arkivkørsel: %w", err)
	}
	return func() { syscall.CloseHandle(handle) }, nil
}
