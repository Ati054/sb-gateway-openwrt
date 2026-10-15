package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Package builds use -mod=vendor; a local replace alone does not select the
// maintained fork. Fail before release if a source fix was not mirrored.
func TestVendoredXrayMatchesMaintainedFork(t *testing.T) {
	root := filepath.Join("..", "..")
	fork := filepath.Join(root, "third_party", "xray-core")
	vendor := filepath.Join(root, "vendor", "github.com", "xtls", "xray-core")
	checked := 0
	err := filepath.WalkDir(vendor, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		name, err := filepath.Rel(vendor, path)
		if err != nil {
			return err
		}
		source, err := os.ReadFile(filepath.Join(fork, name))
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(source, actual) {
			t.Errorf("vendored Xray differs from maintained fork: %s", name)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("unexpectedly incomplete vendored Xray: %d Go files", checked)
	}
}
