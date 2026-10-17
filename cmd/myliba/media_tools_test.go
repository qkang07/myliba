package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundledMediaToolPath(t *testing.T) {
	executableDir := t.TempDir()
	if path := bundledMediaToolPath(executableDir, "ffmpeg"); path != "" {
		t.Fatalf("unexpected media tool before bundling: %q", path)
	}
	toolDir := filepath.Join(executableDir, "ffmpeg")
	if err := os.Mkdir(toolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := "ffmpeg"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	expected := filepath.Join(toolDir, binary)
	if err := os.WriteFile(expected, []byte("tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	if path := bundledMediaToolPath(executableDir, "ffmpeg"); path != expected {
		t.Fatalf("bundled media tool = %q, want %q", path, expected)
	}
}

// Compile the test binary into a portable release folder and set
// MYLIBA_TEST_PORTABLE=1 to verify executable-relative discovery there.
func TestPortableMediaToolDiscovery(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("MYLIBA_TEST_PORTABLE") != "1" {
		t.Skip("portable Windows bundle test")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		want := filepath.Join(filepath.Dir(executable), "ffmpeg", name+".exe")
		if got := mediaToolPath(name); got != want {
			t.Fatalf("%s path = %q, want %q", name, got, want)
		}
	}
}
