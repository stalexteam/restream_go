package mtx

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Мінімуми з (MIN_CONNECT_TIMEOUT_MS/MIN_READ_TIMEOUT_MS);
// settings_store ще не портований — дубльовано тут, нотатка.
const (
	MinConnectTimeoutMS int64 = 2500
	MinReadTimeoutMS    int64 = 300
)

var (
	readTimeoutLineRe  = regexp.MustCompile(`(?m)^readTimeout:[ \t]*\S+[ \t]*$`)
	rtmpAddressLineRe  = regexp.MustCompile(`(?m)^rtmpAddress:[ \t]*\S+[ \t]*$`)
	srtAddressLineRe   = regexp.MustCompile(`(?m)^srtAddress:[ \t]*\S+[ \t]*$`)
	defaultHookAddress = "127.0.0.1:8790"
)

// Дефолти = значення в шаблоні, тож без ключів підстановка тотожна.
const (
	DefaultRTMPPort   int64 = 1935
	DefaultSRTPort    int64 = 8890
	DefaultListenPort int64 = 8790
)

// Render рендерить mediamtx.yml із текстом шаблону + config у outPath,
// атомарно (tmp+rename). Повертає error, якщо шаблон не містить рядок
// readTimeout:.
func Render(templateText, outPath string, config map[string]any) error {
	// Порти йдуть ДО паролів: інакше пароль із текстом хук-адреси всередині
	// сам би підмінився.
	text := renderPorts(templateText, config)
	// Один прохід на обидва токени: підставлений пароль більше не є входом
	// для наступної підміни.
	text = strings.NewReplacer(
		"__OBS_PASS__", pyStrField(config, "obs_pass"),
		"__INTERNAL_PASS__", pyStrField(config, "internal_pass"),
	).Replace(text)

	totalMS := readTimeoutMS(config)
	newText, n := replaceFirst(readTimeoutLineRe, text, "readTimeout: "+strconv.FormatInt(totalMS, 10)+"ms")
	if n == 0 {
		return fmt.Errorf("'readTimeout:' line not found in the mediamtx template -- refusing to render")
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	tmp := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".tmp" + filepath.Ext(outPath)
	if err := os.WriteFile(tmp, []byte(newText), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, outPath)
}

// renderPorts — стендові/користувацькі порти шаблону (девіація EL18):
// rtmpAddress, srtAddress і адреса хуків runOnAvailable/runOnUnavailable.
func renderPorts(text string, config map[string]any) string {
	rtmp := portOrDefault(config, "mediamtx_rtmp_port", DefaultRTMPPort)
	text, _ = replaceFirst(rtmpAddressLineRe, text, "rtmpAddress: :"+strconv.FormatInt(rtmp, 10))
	srt := portOrDefault(config, "mediamtx_srt_port", DefaultSRTPort)
	text, _ = replaceFirst(srtAddressLineRe, text, "srtAddress: :"+strconv.FormatInt(srt, 10))
	listen := portOrDefault(config, "listen_port", DefaultListenPort)
	return strings.ReplaceAll(text, defaultHookAddress,
		"127.0.0.1:"+strconv.FormatInt(listen, 10))
}

func portOrDefault(config map[string]any, key string, def int64) int64 {
	n, ok := asPyInt(config[key])
	if !ok {
		return def
	}
	return n
}

// readTimeoutMS — сума клампнутих connect/read; значення, чужі
// python isinstance(int) (float/рядок/bool/відсутнє), падають до мінімуму.
func readTimeoutMS(config map[string]any) int64 {
	return clampOrMin(config, "connect_timeout_ms", MinConnectTimeoutMS) +
		clampOrMin(config, "read_timeout_ms", MinReadTimeoutMS)
}

func clampOrMin(config map[string]any, key string, min int64) int64 {
	n, ok := asPyInt(config[key])
	if !ok || n < min {
		return min
	}
	return n
}

func asPyInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	default:
		return 0, false
	}
}

// pyStrField — config.get(key, "") із падінням на "" для nil (python.get
// дає ""-default), панікою на truthy не-рядок (python str.replace(old,
// non_str) кидає TypeError — нотатка).
func pyStrField(config map[string]any, key string) string {
	v, ok := config[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		panic(fmt.Sprintf("mtx: expected a string for config[%q], got %T (python-parity crash)", key, v))
	}
	return s
}

// replaceFirst — re.subn(pattern, repl, text, count=1): підмінює лише
// ПЕРШЕ входження, повертає нову кількість замін.
func replaceFirst(re *regexp.Regexp, text, repl string) (string, int) {
	loc := re.FindStringIndex(text)
	if loc == nil {
		return text, 0
	}
	return text[:loc[0]] + repl + text[loc[1]:], 1
}
