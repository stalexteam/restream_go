package control

// registry — реєстр із порядком вставки (python-dict): Set нового ключа додає
// його в кінець, Pop+Set знову ставить у кінець (саме так рухається запис при
// rename/recreate, і від цього залежить порядок персисту).
type registry[T any] struct {
	keys []string
	vals map[string]T
}

func newRegistry[T any]() *registry[T] {
	return &registry[T]{vals: map[string]T{}}
}

func (r *registry[T]) Get(key string) (T, bool) {
	v, ok := r.vals[key]
	return v, ok
}

func (r *registry[T]) Has(key string) bool {
	_, ok := r.vals[key]
	return ok
}

func (r *registry[T]) Set(key string, val T) {
	if _, ok := r.vals[key]; !ok {
		r.keys = append(r.keys, key)
	}
	r.vals[key] = val
}

func (r *registry[T]) Pop(key string) {
	if _, ok := r.vals[key]; !ok {
		return
	}
	delete(r.vals, key)
	for i, k := range r.keys {
		if k == key {
			r.keys = append(r.keys[:i], r.keys[i+1:]...)
			break
		}
	}
}

func (r *registry[T]) Keys() []string {
	return append([]string(nil), r.keys...)
}

func (r *registry[T]) Values() []T {
	out := make([]T, 0, len(r.keys))
	for _, k := range r.keys {
		out = append(out, r.vals[k])
	}
	return out
}

func (r *registry[T]) Len() int { return len(r.keys) }
