package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"restream_go/internal/jsonwire"
)

// Persist — атомарний запис config у path: tmp-файл + rename, як
// settings_store.persist.
func Persist(path string, config *Dict) error {
	tmp := strings.TrimSuffix(path, filepath.Ext(path)) + ".tmp" + filepath.Ext(path)
	if err := os.WriteFile(tmp, Dumps(config), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Loads — розбір config.json у Dict із ЗБЕРЕЖЕНИМ порядком ключів; цілі числа
// стають int64, дробові — float64.
func Loads(raw []byte) (*Dict, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	d, ok := value.(*Dict)
	if !ok {
		return nil, fmt.Errorf("control: config.json is not an object")
	}
	return d, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeFrom(dec, token)
}

func decodeFrom(dec *json.Decoder, token json.Token) (any, error) {
	switch t := token.(type) {
	case json.Delim:
		if t == '{' {
			d := NewDict()
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return nil, err
				}
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				d.Set(key.(string), value)
			}
			_, err := dec.Token()
			return d, err
		}
		items := []any{}
		for dec.More() {
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		_, err := dec.Token()
		return items, err
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, nil
		}
		return t.Float64()
	default:
		return token, nil
	}
}

// Dumps — байти, ідентичні python `json.dump(config, f, indent=2)` +
// трейлінговий "\n" (ensure_ascii, без sort_keys).
func Dumps(config *Dict) []byte {
	var b strings.Builder
	writeIndented(&b, config, 0)
	b.WriteByte('\n')
	return []byte(b.String())
}

func writeIndented(b *strings.Builder, v any, depth int) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(pyFloatRepr(x))
	case string:
		jsonwire.WriteString(b, x)
	case *Dict:
		writeDictIndented(b, x, depth)
	case []any:
		writeListIndented(b, x, depth)
	case []*Dict:
		items := make([]any, len(x))
		for i, d := range x {
			items[i] = d
		}
		writeListIndented(b, items, depth)
	default:
		panic(fmt.Sprintf("control: Dumps: unsupported type %T", v))
	}
}

func writeDictIndented(b *strings.Builder, d *Dict, depth int) {
	if d.Len() == 0 {
		b.WriteString("{}")
		return
	}
	b.WriteString("{\n")
	inner := strings.Repeat("  ", depth+1)
	for i, key := range d.Keys() {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString(inner)
		jsonwire.WriteString(b, key)
		b.WriteString(": ")
		val, _ := d.Get(key)
		writeIndented(b, val, depth+1)
	}
	b.WriteByte('\n')
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte('}')
}

func writeListIndented(b *strings.Builder, items []any, depth int) {
	if len(items) == 0 {
		b.WriteString("[]")
		return
	}
	b.WriteString("[\n")
	inner := strings.Repeat("  ", depth+1)
	for i, item := range items {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString(inner)
		writeIndented(b, item, depth+1)
	}
	b.WriteByte('\n')
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte(']')
}

// pyFloatRepr — наближення python float.__repr__ (normalize_* float не дають).
func pyFloatRepr(x float64) string {
	s := strconv.FormatFloat(x, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}
