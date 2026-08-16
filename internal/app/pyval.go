package app

import (
	"fmt"
	"strconv"
	"strings"

	"restream_go/internal/control"
)

// pyStr — python `str(value)` для значень json.
func pyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return pyFloatText(typed)
	}
	return fmt.Sprint(value)
}

// pyFloatText — наближення python repr(float) (як FloatRepr).
func pyFloatText(value float64) string {
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case *control.Dict:
		return typed.Len() > 0
	}
	return true
}
