//go:build windows

package main

import (
	"fmt"
	"syscall"
)

func localFileID(path string) string {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	const fileReadAttributes = 0x80
	handle, err := syscall.CreateFile(name, fileReadAttributes,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(handle)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		return ""
	}
	return fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
}
