package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"restream_go/internal/control"
	"restream_go/internal/mtx"
)

// requiredKeys — поля, без яких штатний старт не має сенсу.
var requiredKeys = []string{"listen_host", "listen_port", "obs_pass", "internal_pass", "dashboard_token"}

// requiredTools — зовнішні бінарі конвеєра; шукаються в PATH, куди Main уже
// додав <base>/bin.
var requiredTools = []string{"ffmpeg", "ffprobe"}

// lookPath — шов для тестів.
var lookPath = exec.LookPath

// startupError несе і причину відмови, і підказку, як її полагодити.
type startupError struct{ problem, hint string }

func (e *startupError) Error() string { return e.problem + "\n" + e.hint }

const hintRunConfig = "run ./restreamd --config (or install.sh) to create or repair it"

// validateStartup — строга перевірка перед стартом демона (девіація від
// толерантної перевірки, що пропускала і відсутній mediamtx, і недоступну заглушку).
func validateStartup(baseDir, configPath string) (*control.Dict, error) {
	config, err := loadStartupConfig(configPath)
	if err != nil {
		return nil, err
	}
	if err := checkRequiredKeys(configPath, config); err != nil {
		return nil, err
	}
	if err := checkMediaMTXBinary(baseDir); err != nil {
		return nil, err
	}
	if err := checkRequiredTools(baseDir); err != nil {
		return nil, err
	}
	return config, nil
}

func checkRequiredTools(baseDir string) error {
	for _, name := range requiredTools {
		if _, err := lookPath(name); err != nil {
			return &startupError{"required program not found: " + name,
				"install ffmpeg, or put " + name + " in " + filepath.Join(baseDir, "bin")}
		}
	}
	return nil
}

func loadStartupConfig(configPath string) (*control.Dict, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &startupError{"config file not found: " + configPath, hintRunConfig}
		}
		return nil, &startupError{fmt.Sprintf("could not read the config file %s: %v", configPath, err), hintRunConfig}
	}
	config, err := control.Loads(raw)
	if err != nil {
		return nil, &startupError{fmt.Sprintf("the config file %s is not valid JSON: %v", configPath, err), hintRunConfig}
	}
	return config, nil
}

func checkRequiredKeys(configPath string, config *control.Dict) error {
	for _, key := range requiredKeys {
		value, ok := config.Get(key)
		if !ok || !pyTruthy(value) {
			return &startupError{
				fmt.Sprintf("the config file %s has no value for '%s'", configPath, key), hintRunConfig}
		}
		if text := strings.TrimSpace(pyStr(value)); text == "" || isPlaceholder(text) {
			return &startupError{
				fmt.Sprintf("the config file %s still carries the placeholder value %q for '%s'",
					configPath, text, key), hintRunConfig}
		}
	}
	return nil
}

func checkMediaMTXBinary(baseDir string) error {
	path := mtx.BinaryPath(baseDir)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return &startupError{"the MediaMTX binary is missing: " + path,
			"run ./install.sh to download it, or put the mediamtx binary there yourself"}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return &startupError{"the MediaMTX binary is not executable: " + path, "chmod +x " + path}
	}
	return nil
}
