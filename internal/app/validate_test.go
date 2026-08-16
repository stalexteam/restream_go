package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"restream_go/internal/control"
)

// validBase — макет інсталяції, який має проходити строгу валідацію.
func validBase(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	if err := ensureDirs(base); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "bin", "mediamtx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, "config.json")
	if err := control.Persist(configPath, validConfig()); err != nil {
		t.Fatal(err)
	}
	return base, configPath
}

func validConfig() *control.Dict {
	return control.D(
		"listen_host", "127.0.0.1",
		"listen_port", int64(8790),
		"public_host", "vps.example",
		"obs_pass", "aa",
		"internal_pass", "bb",
		"dashboard_token", "cc",
	)
}

func mustPersist(t *testing.T, path string, config *control.Dict) {
	t.Helper()
	if err := control.Persist(path, config); err != nil {
		t.Fatal(err)
	}
}

func TestValidateStartupAcceptsCompleteLayout(t *testing.T) {
	base, configPath := validBase(t)
	if _, err := validateStartup(base, configPath); err != nil {
		t.Fatalf("validateStartup on a complete layout: %v", err)
	}
}

func TestValidateStartupRejectsMissingConfig(t *testing.T) {
	base, configPath := validBase(t)
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	assertStartupError(t, base, configPath, "config file not found", "--config")
}

func TestValidateStartupRejectsBrokenJSON(t *testing.T) {
	base, configPath := validBase(t)
	if err := os.WriteFile(configPath, []byte("{ nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertStartupError(t, base, configPath, "not valid JSON", "--config")
}

func TestValidateStartupRejectsEmptyAndPlaceholderSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{"empty", "obs_pass", "", "has no value for 'obs_pass'"},
		{"placeholder", "dashboard_token", "__DASHBOARD_TOKEN__", "placeholder value"},
		{"missing host", "listen_host", "", "has no value for 'listen_host'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, configPath := validBase(t)
			config := validConfig()
			config.Set(tc.key, tc.value)
			mustPersist(t, configPath, config)
			assertStartupError(t, base, configPath, tc.want, "--config")
		})
	}
}

func TestValidateStartupRejectsMissingMediaMTX(t *testing.T) {
	base, configPath := validBase(t)
	if err := os.Remove(filepath.Join(base, "bin", "mediamtx")); err != nil {
		t.Fatal(err)
	}
	assertStartupError(t, base, configPath, "MediaMTX binary is missing", "install.sh")
}

func TestValidateStartupRejectsNonExecutableMediaMTX(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores the executable bit")
	}
	base, configPath := validBase(t)
	if err := os.Chmod(filepath.Join(base, "bin", "mediamtx"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertStartupError(t, base, configPath, "not executable", "chmod +x")
}

// Оракульна толерантність збережена: порожній media/ старту не заважає.
func TestValidateStartupToleratesEmptyMedia(t *testing.T) {
	base, configPath := validBase(t)
	if _, err := validateStartup(base, configPath); err != nil {
		t.Fatalf("empty media/ must not block the start: %v", err)
	}
}

func assertStartupError(t *testing.T, base, configPath, wantProblem, wantHint string) {
	t.Helper()
	_, err := validateStartup(base, configPath)
	if err == nil {
		t.Fatalf("validateStartup passed, want an error mentioning %q", wantProblem)
	}
	text := err.Error()
	if !strings.Contains(text, wantProblem) {
		t.Errorf("error = %q, want it to mention %q", text, wantProblem)
	}
	if !strings.Contains(text, wantHint) {
		t.Errorf("error = %q, want the hint to mention %q", text, wantHint)
	}
}
