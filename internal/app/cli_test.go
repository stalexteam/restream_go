// Unix-only: перевіряє коди виходу зібраного бінаря.
//go:build !windows

package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type cliRun struct {
	stdout, stderr string
	code           int
}

func runCLI(t *testing.T, binary, stdin string, args ...string) cliRun {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return cliRun{out.String(), errBuf.String(), code}
}

// cliBinary кладе бінар у base (не в bin/), щоб baseDir дорівнював base.
func cliBinary(t *testing.T, base string) string {
	t.Helper()
	out := filepath.Join(base, "restreamd")
	build := exec.Command(goTool(t), "build", "-o", out, "restream_go")
	var stderr bytes.Buffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v\n%s", err, stderr.String())
	}
	return out
}

// TestCLIRejectsUnknownArguments — усе, крім --config//config, це usage + exit 2.
func TestCLIRejectsUnknownArguments(t *testing.T) {
	base := scratchBase(t)
	binary := cliBinary(t, base)
	for _, args := range [][]string{{"cfg"}, {"gen-secret"}, {"--help"}, {"/etc/config.json"}, {"--config", "extra"}} {
		run := runCLI(t, binary, "", args...)
		if run.code != 2 {
			t.Errorf("%v: exit=%d, want 2 (stderr=%q)", args, run.code, run.stderr)
		}
		if !strings.Contains(run.stderr, "usage: restreamd") {
			t.Errorf("%v: stderr=%q, want a usage line", args, run.stderr)
		}
	}
}

// TestCLIStartWithoutConfigExits — штатний старт без конфіга: exit 1 і порада.
func TestCLIStartWithoutConfigExits(t *testing.T) {
	base := scratchBase(t)
	binary := cliBinary(t, base)
	run := runCLI(t, binary, "")
	if run.code != 1 {
		t.Errorf("exit=%d, want 1 (stderr=%q)", run.code, run.stderr)
	}
	for _, want := range []string{"config file not found", "--config"} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("stderr=%q, want it to mention %q", run.stderr, want)
		}
	}
}

// TestCLISetupModeIsIdempotent — --config створює все, повторний запуск із
// порожнім вводом нічого не змінює.
func TestCLISetupModeIsIdempotent(t *testing.T) {
	base := scratchBase(t)
	binary := cliBinary(t, base)

	run := runCLI(t, binary, "myhost\n", "--config")
	if run.code != 0 {
		t.Fatalf("--config exit=%d, stderr=%q", run.code, run.stderr)
	}
	if !strings.Contains(run.stdout, "http://myhost:8790/dashboard?token=") {
		t.Errorf("summary has no dashboard link:\n%s", run.stdout)
	}
	configText := readFile(t, filepath.Join(base, "config.json"))
	if strings.Contains(configText, "__") {
		t.Errorf("placeholders survived in config.json:\n%s", configText)
	}
	if !strings.Contains(configText, `"public_host": "myhost"`) {
		t.Errorf("public_host was not stored:\n%s", configText)
	}
	dock := readFile(t, filepath.Join(base, "obs-dock.html"))
	if !strings.Contains(dock, "http://myhost:8790/dashboard?token=") {
		t.Errorf("obs-dock.html has no rendered URL")
	}
	if _, err := os.Stat(filepath.Join(base, "obs-source.html")); err != nil {
		t.Errorf("obs-source.html was not written: %v", err)
	}

	if second := runCLI(t, binary, "\n", "--config"); second.code != 0 {
		t.Fatalf("second --config exit=%d, stderr=%q", second.code, second.stderr)
	}
	if after := readFile(t, filepath.Join(base, "config.json")); after != configText {
		t.Errorf("config.json changed on the second --config\nbefore:\n%s\nafter:\n%s", configText, after)
	}
	if slash := runCLI(t, binary, "\n", "/config"); slash.code != 0 {
		t.Errorf("/config exit=%d, stderr=%q", slash.code, slash.stderr)
	}
}

// TestCLISetupModeDoesNotStartDaemon — після --config немає ні tmp/mediamtx.yml,
// ні pid-файлу.
func TestCLISetupModeDoesNotStartDaemon(t *testing.T) {
	base := scratchBase(t)
	binary := cliBinary(t, base)
	if run := runCLI(t, binary, "myhost\n", "--config"); run.code != 0 {
		t.Fatalf("--config exit=%d, stderr=%q", run.code, run.stderr)
	}
	for _, name := range []string{"tmp/mediamtx.yml", "tmp/.mediamtx.pid"} {
		if _, err := os.Stat(filepath.Join(base, filepath.FromSlash(name))); err == nil {
			t.Errorf("%s exists -- the setup mode started the controller", name)
		}
	}
}
