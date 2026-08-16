package control

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// pyTruthy — python bool(v): 0/0.0/""/nil/false/порожні list-и й Dict-и хибні.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) != 0
	case *Dict:
		return x.Len() != 0
	default:
		return true
	}
}

// pyOr — python `a or b`: перший truthy операнд.
func pyOr(a, b any) any {
	if pyTruthy(a) {
		return a
	}
	return b
}

var pyIntStringRe = regexp.MustCompile(`^[+-]?[0-9]+$`)

// pyInt — python int(v): TypeError/ValueError -> ok=false. Синтаксично
// валідні, але за межами int64 рядки клампляться до MaxInt64/MinInt64
func pyInt(v any) (int64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		if x >= math.MaxInt64 {
			return math.MaxInt64, true
		}
		if x <= math.MinInt64 {
			return math.MinInt64, true
		}
		return int64(x), true
	case string:
		s := strings.TrimSpace(x)
		if !pyIntStringRe.MatchString(s) {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			if strings.HasPrefix(s, "-") {
				return math.MinInt64, true
			}
			return math.MaxInt64, true
		}
		return n, true
	default:
		return 0, false
	}
}

// pyEqual — python `==` для значень конфіга: числа порівнюються за значенням
// (bool окремим типом, як у python лише int/bool сумісні), списки поелементно.
func pyEqual(a, b any) bool {
	la, aIsList := a.([]any)
	lb, bIsList := b.([]any)
	if aIsList || bIsList {
		if !aIsList || !bIsList || len(la) != len(lb) {
			return false
		}
		for i := range la {
			if !pyEqual(la[i], lb[i]) {
				return false
			}
		}
		return true
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ab, aIsBool := a.(bool)
	bb, bIsBool := b.(bool)
	if aIsBool || bIsBool {
		if aIsBool && bIsBool {
			return ab == bb
		}
		return false
	}
	ia, aIsInt := pyInt(a)
	ib, bIsInt := pyInt(b)
	if aIsInt && bIsInt {
		_, aStr := a.(string)
		_, bStr := b.(string)
		if !aStr && !bStr {
			return ia == ib
		}
	}
	return a == b
}

// pyStr — поле, що в python-коді мусить бути str. Truthy не-рядок панікує,
// як python AttributeError на `.strip`/`re.sub`.
func pyStr(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		if pyTruthy(v) {
			panic("control: expected a string field, got a non-string truthy value (python-parity crash)")
		}
		return ""
	}
	return s
}
