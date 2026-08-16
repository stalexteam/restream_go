package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"restream_go/internal/control"
)

// requiredKeys — поля, без яких штатний старт не має сенсу.
var requiredKeys = []string{"listen_host", "listen_port", "obs_pass", "internal_pass", "dashboard_token"}

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
	return config, nil
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
	path := filepath.Join(baseDir, "bin", "mediamtx")
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
