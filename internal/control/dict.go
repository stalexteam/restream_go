// Package control — control plane: Manager зі (реєстри, хуки,
// контракт, сесія, CRUD) і нормалізація config.json.
package control

import (
	"encoding/json"
	"strings"

	"restream_go/internal/jsonwire"
)

// Dict — мапа з порядком вставки ключів: від цього порядку залежать байти
// персисту. Мутується на місці тим самим *Dict, що й у викликача.
type Dict struct {
	keys []string
	vals map[string]any
}

// NewDict — порожній словник.
func NewDict() *Dict {
	return &Dict{vals: map[string]any{}}
}

// D будує Dict з пар ключ/значення в заданому порядку — еквівалент
// python-літерала `{...}` у місцях, де треба зібрати дефолтний dict.
func D(pairs ...any) *Dict {
	d := NewDict()
	for i := 0; i+1 < len(pairs); i += 2 {
		d.Set(pairs[i].(string), pairs[i+1])
	}
	return d
}

// Get — як python `d.get(key)`: (nil, false), якщо ключа немає.
func (d *Dict) Get(key string) (any, bool) {
	v, ok := d.vals[key]
	return v, ok
}

// GetOr — як python `d.get(key, def)`.
func (d *Dict) GetOr(key string, def any) any {
	if v, ok := d.vals[key]; ok {
		return v
	}
	return def
}

// Has — чи є ключ у словнику (незалежно від значення, навіть nil/false/0).
func (d *Dict) Has(key string) bool {
	_, ok := d.vals[key]
	return ok
}

// Set — як python `d[key] = val`: наявний ключ зберігає позицію, новий
// додається в кінець порядку вставки.
func (d *Dict) Set(key string, val any) {
	if _, ok := d.vals[key]; !ok {
		d.keys = append(d.keys, key)
	}
	d.vals[key] = val
}

// SetDefault — як python `d.setdefault(key, val)`: не чіпає наявний ключ,
// навіть якщо його значення falsy.
func (d *Dict) SetDefault(key string, val any) any {
	if v, ok := d.vals[key]; ok {
		return v
	}
	d.Set(key, val)
	return val
}

// Pop — як python `d.pop(key, None)`: тихий no-op, якщо ключа немає.
func (d *Dict) Pop(key string) {
	if _, ok := d.vals[key]; !ok {
		return
	}
	delete(d.vals, key)
	for i, k := range d.keys {
		if k == key {
			d.keys = append(d.keys[:i], d.keys[i+1:]...)
			break
		}
	}
}

// Clone — поверхнева копія з тим самим порядком ключів (python `dict(d)`).
func (d *Dict) Clone() *Dict {
	out := NewDict()
	for _, k := range d.keys {
		out.Set(k, d.vals[k])
	}
	return out
}

// MarshalJSON — компактний JSON у порядку вставки (форма C2 для hub-а).
func (d *Dict) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range d.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		jsonwire.WriteString(&b, k)
		b.WriteByte(':')
		raw, err := json.Marshal(d.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(raw)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// Keys — ключі в порядку вставки (для персисту).
func (d *Dict) Keys() []string {
	return d.keys
}

// Len — кількість ключів.
func (d *Dict) Len() int {
	return len(d.keys)
}
