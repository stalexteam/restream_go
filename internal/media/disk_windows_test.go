//go:build windows

package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiskFreeLive(t *testing.T) {
	free, ok := diskFree(os.TempDir())
	if !ok || free == 0 {
		t.Fatalf("diskFree(TempDir) = %d, %v", free, ok)
	}
	if _, ok := diskFree(filepath.Join(os.TempDir(), "restream-no-such-dir-8f2c")); ok {
		t.Fatal("diskFree(missing dir): ok=true")
	}
}
