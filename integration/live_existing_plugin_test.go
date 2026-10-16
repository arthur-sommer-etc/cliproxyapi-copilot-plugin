//go:build !windows

package integration

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func livePluginExtension() string {
	if runtime.GOOS == "darwin" {
		return ".dylib"
	}
	return ".so"
}

// The opt-in path loads an existing artifact in place without staging or installation.
func validateLiveExistingPluginDirectory(directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("directory must be absolute")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return errors.New("directory must already exist and cannot be a symlink")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	expected := "cliproxyapi-copilot" + livePluginExtension()
	found := false
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("directory must be flat to prevent nested plugin discovery")
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), livePluginExtension()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || entry.Name() != expected {
			return errors.New("directory contains an unexpected native artifact")
		}
		found = true
	}
	if !found {
		return errors.New("expected native artifact is missing")
	}
	return nil
}

func TestLiveExistingPluginDirectoryIsolation(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "cliproxyapi-copilot"+livePluginExtension())
	if err := os.WriteFile(artifact, []byte("synthetic artifact for directory validation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateLiveExistingPluginDirectory(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"other" + livePluginExtension(), "nested"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			var err error
			if name == "nested" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.Symlink(artifact, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			if validateLiveExistingPluginDirectory(root) == nil {
				t.Fatal("unexpected loader candidate was accepted")
			}
		})
	}
	if validateLiveExistingPluginDirectory("relative") == nil {
		t.Fatal("relative directory was accepted")
	}
}
