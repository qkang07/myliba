package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// mediaToolPath prefers the tools shipped with a portable Myliba build.
// Development runs can continue to use tools installed on PATH.
func mediaToolPath(name string) string {
	if executable, err := os.Executable(); err == nil {
		if path := bundledMediaToolPath(filepath.Dir(executable), name); path != "" {
			return path
		}
	}
	path, _ := exec.LookPath(name)
	return path
}

func bundledMediaToolPath(executableDir, name string) string {
	binary := name
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	bundled := filepath.Join(executableDir, "ffmpeg", binary)
	path, _ := exec.LookPath(bundled)
	return path
}
