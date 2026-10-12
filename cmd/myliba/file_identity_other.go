//go:build !windows && !darwin && !freebsd && !linux && !netbsd && !openbsd

package main

func localFileID(_ string) string { return "" }
