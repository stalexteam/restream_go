package media

import (
	"os"
	"path/filepath"
	"testing"
)

// mustWrite — тестовий файл заданого розміру.
func mustWrite(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupTree — коренева тека з кількома кліпами; другим значенням — шляхи файлів.
func setupTree(t *testing.T) (string, []string) {
	t.Helper()
	root := t.TempDir()
	files := []string{"a.mp4", "clips/one.mp4", "clips/two.mkv"}
	for _, rel := range files {
		mustWrite(t, filepath.Join(root, rel), 16)
	}
	return root, files
}
