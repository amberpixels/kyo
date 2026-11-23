package callback

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallWritesOnceAndReuses(t *testing.T) {
	root := t.TempDir()

	dir, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, Name+".py")
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, Source()) {
		t.Fatal("installed plugin differs from the embedded source")
	}

	st1, _ := os.Stat(file)
	dir2, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(file)
	if dir2 != dir || !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("second install should reuse the existing file")
	}
}
