package app

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupLoggingRelativePath -- CD3: відносний log_file відкривається
// відносно поточного CWD процесу (як python FileHandler), а не base dir.
func TestSetupLoggingRelativePath(t *testing.T) {
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	origFlags := log.Flags()
	t.Cleanup(func() {
		if err := os.Chdir(origWD); err != nil {
			t.Fatal(err)
		}
		log.SetFlags(origFlags)
		log.SetOutput(os.Stderr)
	})

	workdir := t.TempDir()
	decoy := t.TempDir()
	if err := os.Chdir(workdir); err != nil {
		t.Fatal(err)
	}

	if err := setupLogging("relative.log"); err != nil {
		t.Fatalf("setupLogging: %v", err)
	}
	log.Print("hello from relative log test")

	wantPath := filepath.Join(workdir, "relative.log")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("expected log file at CWD-relative path %s: %v", wantPath, err)
	}
	if !strings.Contains(string(data), "hello from relative log test") {
		t.Errorf("log file content = %q, missing expected message", data)
	}
	if _, err := os.Stat(filepath.Join(decoy, "relative.log")); err == nil {
		t.Errorf("relative.log unexpectedly created in unrelated dir %s", decoy)
	}
}
