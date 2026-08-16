package app

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"restream_go/internal/control"
)

var hex32Re = regexp.MustCompile(`^[0-9a-f]{32}$`)

func setupBase(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	log.SetOutput(os.Stderr)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return base, filepath.Join(base, "config.json")
}

// TestEnsureConfigCreatesFresh — відсутній конфіг створюється з вшитого
// прикладу, з реальними секретами замість плейсхолдерів.
func TestEnsureConfigCreatesFresh(t *testing.T) {
	base, configPath := setupBase(t)

	config, err := ensureConfig(base, configPath)
	if err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	for _, placeholder := range []string{"__OBS_PASS__", "__INTERNAL_PASS__", "__DASHBOARD_TOKEN__", "__BASE_DIR__"} {
		if strings.Contains(string(raw), placeholder) {
			t.Errorf("%s survived in the created config", placeholder)
		}
	}
	seen := map[string]bool{}
	for _, key := range secretKeys {
		value := pyStr(config.GetOr(key, ""))
		if !hex32Re.MatchString(value) {
			t.Errorf("%s = %q, want 32 hex chars", key, value)
		}
		if seen[value] {
			t.Errorf("%s reuses another secret's value", key)
		}
		seen[value] = true
	}
	if got := pyStr(config.GetOr("public_host", "")); got != placeholderHost {
		t.Errorf("public_host = %q, want %q", got, placeholderHost)
	}
	if got := pyStr(config.GetOr("listen_port", "")); got != defaultPort {
		t.Errorf("listen_port = %q, want %q", got, defaultPort)
	}
}

// TestEnsureConfigKeepsExistingValues — повторний виклик нічого не переписує.
func TestEnsureConfigKeepsExistingValues(t *testing.T) {
	base, configPath := setupBase(t)
	first, err := ensureConfig(base, configPath)
	if err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	second, err := ensureConfig(base, configPath)
	if err != nil {
		t.Fatalf("second ensureConfig: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("config was rewritten on the second run\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, key := range append([]string{"public_host", "listen_port"}, secretKeys...) {
		if pyStr(first.GetOr(key, "")) != pyStr(second.GetOr(key, "")) {
			t.Errorf("%s changed between runs", key)
		}
	}
}

// TestEnsureConfigBackupsBrokenJSON — битий JSON їде в.broken-<ts>, поруч
// зʼявляється свіжий конфіг.
func TestEnsureConfigBackupsBrokenJSON(t *testing.T) {
	base, configPath := setupBase(t)
	if err := os.WriteFile(configPath, []byte("{ not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := ensureConfig(base, configPath)
	if err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}
	if !hex32Re.MatchString(pyStr(config.GetOr("obs_pass", ""))) {
		t.Errorf("fresh config has no generated obs_pass")
	}
	matches, err := filepath.Glob(configPath + ".broken-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one .broken-* backup, got %v", matches)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{ not json at all" {
		t.Errorf("backup content = %q, the original was not preserved", raw)
	}
}

// TestEnsureConfigFillsOnlyMissingKeys — наявні значення недоторкані, бракітні
// ключі доливаються з прикладу.
func TestEnsureConfigFillsOnlyMissingKeys(t *testing.T) {
	base, configPath := setupBase(t)
	original := control.D(
		"listen_port", int64(9999),
		"public_host", "example.org",
		"obs_pass", "kept-pass",
		"dashboard_token", "__DASHBOARD_TOKEN__",
	)
	if err := control.Persist(configPath, original); err != nil {
		t.Fatal(err)
	}

	config, err := ensureConfig(base, configPath)
	if err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}
	if got := pyStr(config.GetOr("listen_port", "")); got != "9999" {
		t.Errorf("listen_port = %q, want the existing 9999", got)
	}
	if got := pyStr(config.GetOr("public_host", "")); got != "example.org" {
		t.Errorf("public_host = %q, want the existing example.org", got)
	}
	if got := pyStr(config.GetOr("obs_pass", "")); got != "kept-pass" {
		t.Errorf("obs_pass = %q, want the existing value", got)
	}
	if got := pyStr(config.GetOr("dashboard_token", "")); !hex32Re.MatchString(got) {
		t.Errorf("dashboard_token = %q, the placeholder was not replaced", got)
	}
	for _, key := range []string{"listen_host", "internal_pass", "sources", "platforms", "fallback_presets"} {
		if !config.Has(key) {
			t.Errorf("missing key %q was not filled in from the example", key)
		}
	}

	// Долив persist-иться: наступний старт бачить той самий конфіг.
	reread, err := ensureConfig(base, configPath)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got := pyStr(reread.GetOr("listen_port", "")); got != "9999" {
		t.Errorf("listen_port after persist = %q", got)
	}
}

// TestWriteOBSFiles — URL-и зібрані з public_host/listen_port/токена.
func TestWriteOBSFiles(t *testing.T) {
	base, configPath := setupBase(t)
	config, err := ensureConfig(base, configPath)
	if err != nil {
		t.Fatal(err)
	}
	config.Set("public_host", "vps.example")
	config.Set("listen_port", int64(8123))
	config.Set("dashboard_token", "abc123")

	if err := writeOBSFiles(base, config); err != nil {
		t.Fatalf("writeOBSFiles: %v", err)
	}
	dock, err := os.ReadFile(filepath.Join(base, "obs-dock.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dock), "http://vps.example:8123/dashboard?token=abc123") {
		t.Errorf("obs-dock.html has no rendered dashboard URL")
	}
	if strings.Contains(string(dock), "__DASHBOARD_URL__") {
		t.Errorf("obs-dock.html still carries the placeholder")
	}
	source, err := os.ReadFile(filepath.Join(base, "obs-source.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "ws://vps.example:8123/ws?token=abc123") {
		t.Errorf("obs-source.html has no rendered ws URL")
	}
	if strings.Contains(string(source), "__WS_URL__") {
		t.Errorf("obs-source.html still carries the placeholder")
	}
}

// TestPromptPublicHost — дефолт лишається на порожньому вводі, EOF і сміттi.
func TestPromptPublicHost(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"typed", "vps.example\n", "vps.example"},
		{"trimmed", "  1.2.3.4  \n", "1.2.3.4"},
		{"ipv6", "::1\n", "::1"},
		{"empty", "\n", "old.host"},
		{"eof", "", "old.host"},
		{"leading dash", "-nope\n", "old.host"},
		{"spaces inside", "a b\n", "old.host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			if got := promptPublicHost(strings.NewReader(tc.input), &out, "old.host"); got != tc.want {
				t.Errorf("promptPublicHost(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if !strings.Contains(out.String(), "[old.host]") {
				t.Errorf("prompt did not show the current value: %q", out.String())
			}
		})
	}
}

// TestRunSetupWritesEverything — --config кладе конфіг, OBS-файли і друкує
// підсумок з реальними URL.
func TestRunSetupWritesEverything(t *testing.T) {
	base, configPath := setupBase(t)
	var out strings.Builder
	if code := runSetup(base, configPath, strings.NewReader("vps.example\n"), &out); code != 0 {
		t.Fatalf("runSetup = %d", code)
	}

	config, err := control.Loads([]byte(readConfigFile(t, configPath)))
	if err != nil {
		t.Fatal(err)
	}
	if got := pyStr(config.GetOr("public_host", "")); got != "vps.example" {
		t.Errorf("public_host = %q", got)
	}
	token := pyStr(config.GetOr("dashboard_token", ""))
	if !hex32Re.MatchString(token) {
		t.Errorf("dashboard_token = %q, want 32 hex chars", token)
	}
	dock, err := os.ReadFile(filepath.Join(base, "obs-dock.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dock), "http://vps.example:8790/dashboard?token="+token) {
		t.Errorf("obs-dock.html URL was not rendered")
	}
	for _, want := range []string{"http://vps.example:8790/dashboard?token=" + token,
		"rebuilt", filepath.Join(base, "obs-dock.html"), filepath.Join(base, "obs-source.html"),
		"CONTROL:", "FIREWALL:", "1935/tcp", "8790/tcp", "8890/udp", "Doc/Setup/Setup.md"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary is missing %q:\n%s", want, out.String())
		}
	}
	// Секрети, крім токена дашборда, у підсумок більше не потрапляють.
	if pass := pyStr(config.GetOr("obs_pass", "")); strings.Contains(out.String(), pass) {
		t.Errorf("summary leaks obs_pass:\n%s", out.String())
	}
}

// TestHintsFor — керування й фаєрвол різні на кожній платформі.
func TestHintsFor(t *testing.T) {
	for _, tc := range []struct {
		goos     string
		wantAny  []string
		wantNone []string
	}{
		{"linux", []string{"systemctl start restreamd", "systemctl stop restreamd",
			"journalctl -u restreamd -f", "ufw allow"}, []string{"netsh", "Ctrl+C"}},
		{"windows", []string{"restreamd.exe", "Ctrl+C", "netsh advfirewall",
			"localport=1935,8790"}, []string{"systemctl", "ufw"}},
		{"darwin", []string{"./restreamd", "Ctrl+C"}, []string{"systemctl", "ufw", "netsh"}},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			hints := hintsFor(tc.goos, filepath.Join("base"), "8790", "1935")
			text := strings.Join(append([]string{hints.start, hints.stop, hints.logs,
				hints.firewall}, hints.notes...), "\n")
			for _, want := range tc.wantAny {
				if !strings.Contains(text, want) {
					t.Errorf("%s hints have no %q:\n%s", tc.goos, want, text)
				}
			}
			for _, unwanted := range tc.wantNone {
				if strings.Contains(text, unwanted) {
					t.Errorf("%s hints mention %q:\n%s", tc.goos, unwanted, text)
				}
			}
			if hints.start == "" || hints.stop == "" || hints.logs == "" {
				t.Errorf("%s hints are incomplete: %+v", tc.goos, hints)
			}
		})
	}
}

// TestRunSetupKeepsSecretsAndHost — повторний --config з порожнім вводом.
func TestRunSetupKeepsSecretsAndHost(t *testing.T) {
	base, configPath := setupBase(t)
	var out strings.Builder
	if code := runSetup(base, configPath, strings.NewReader("first.host\n"), &out); code != 0 {
		t.Fatalf("first runSetup = %d", code)
	}
	before := readConfigFile(t, configPath)

	if code := runSetup(base, configPath, strings.NewReader("\n"), &out); code != 0 {
		t.Fatalf("second runSetup = %d", code)
	}
	if after := readConfigFile(t, configPath); after != before {
		t.Errorf("config.json changed on the second setup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func readConfigFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestEnsureDirs — робочі каталоги створюються порожніми.
func TestEnsureDirs(t *testing.T) {
	base := t.TempDir()
	if err := ensureDirs(base); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	for _, name := range []string{"logs", "tmp", "media"} {
		info, err := os.Stat(filepath.Join(base, name))
		if err != nil || !info.IsDir() {
			t.Errorf("%s/ was not created: %v", name, err)
		}
	}
}
