//go:build darwin || freebsd || linux || netbsd || openbsd

package main

import (
	"fmt"
	"os"
	"syscall"
)

func localFileID(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino)
}
