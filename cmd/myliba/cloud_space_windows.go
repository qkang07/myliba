//go:build windows

package main

import "golang.org/x/sys/windows"

func cloudVolumeSpace(path string) (uint64, uint64, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var available, capacity, free uint64
	err = windows.GetDiskFreeSpaceEx(name, &available, &capacity, &free)
	return capacity, available, err
}
