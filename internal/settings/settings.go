// Package settings — валідація/персист "day-2" полів config.json (Settings
// tab дашборда): порт controller/.
package settings

import (
	"fmt"
	"os"
	"strings"

	"restream_go/internal/control"
)

// Хардкоджені мінімуми -- нижче них або ефект нестабільний (RTMP-рукостискання
// не встигає), або детектор стагнації ловив би нормальний джиттер потоку як
// хибний обрив.
const (
	MinConnectTimeoutMS  = 2500
	MinReadTimeoutMS     = 300
	MinOfflineTimeoutSec = 60
)

// LoadEditable — глобальні System-поля для вкладки Settings.
func LoadEditable(configPath string) (*control.Dict, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	full, err := control.Loads(raw)
	if err != nil {
		return nil, err
	}
	out := control.NewDict()
	v, _ := full.Get("connect_timeout_ms")
	out.Set("connect_timeout_ms", v)
	v, _ = full.Get("read_timeout_ms")
	out.Set("read_timeout_ms", v)
	v, _ = full.Get("offline_timeout_sec")
	out.Set("offline_timeout_sec", v)
	out.Set("icmp_ping", pyTruthy(full.GetOr("icmp_ping", false)))
	out.Set("obs_widget_show_bitrate", pyTruthy(full.GetOr("obs_widget_show_bitrate", false)))
	return out, nil
}

// ValidateSystem — {поле: причина} для невалідних полів; збирає ВСІ помилки
// baseDir -- мертвий параметр.
func ValidateSystem(values *control.Dict, baseDir string) map[string]string {
	errors := map[string]string{}
	validateNumber(values, errors, "connect_timeout_ms", MinConnectTimeoutMS, "ms")
	validateNumber(values, errors, "read_timeout_ms", MinReadTimeoutMS, "ms")
	validateNumber(values, errors, "offline_timeout_sec", MinOfflineTimeoutSec, "seconds")
	return errors
}

func validateNumber(values *control.Dict, errors map[string]string, field string, minimum int, unit string) {
	v, _ := values.Get(field)
	n, ok := pyNumber(v)
	if !ok || n < float64(minimum) {
		errors[field] = fmt.Sprintf("must be a number, at least %d %s", minimum, unit)
	}
}

// ValidateGroupName — валідація імені групи платформ (унікальне, непорожнє).
func ValidateGroupName(name string, existingNames []string) map[string]string {
	return validateName(name, existingNames, "group")
}

func validateName(name string, existingNames []string, what string) map[string]string {
	errors := map[string]string{}
	clean := strings.TrimSpace(name)
	if clean == "" {
		errors["name"] = "name is required"
	} else if containsStr(existingNames, clean) {
		errors["name"] = fmt.Sprintf("a %s named '%s' already exists", what, clean)
	}
	return errors
}

func containsStr(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

// pyNumber — python isinstance(v, (int, float)) and not isinstance(v, bool).
func pyNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	default:
		return 0, false
	}
}

// pyIntStrict — python isinstance(v, int) and not isinstance(v, bool)
// (bool -- підклас int у python, тут явно виключений).
func pyIntStrict(v any) (int64, bool) {
	n, ok := v.(int64)
	return n, ok
}

// pyTruthy — python bool(v) для типів, що трапляються в config.json.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) != 0
	default:
		return true
	}
}
