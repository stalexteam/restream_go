package fallback

import (
	"math/rand"
	"time"
)

// ShuffleBag — folder-пресет: кожен файл програється раз за цикл, у кінці циклу
// решаффл усього пулу. Пул поповнюється по мірі підготовки — щойно
// нормалізований файл підмішується у ВИПАДКОВУ позицію решти мішка.
type ShuffleBag struct {
	ready func() []string
	rnd   *rand.Rand
	bag   []string
	known map[string]bool
}

// NewShuffleBag — ready віддає вже готові (нормалізовані) файли; rnd == nil
// бере власне джерело (інʼєкція — заради відтворюваності тестів).
func NewShuffleBag(ready func() []string, rnd *rand.Rand) *ShuffleBag {
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &ShuffleBag{ready: ready, rnd: rnd, known: map[string]bool{}}
}

// Next — наступний файл циклу; false — готового немає нічого.
func (b *ShuffleBag) Next() (string, bool) {
	ready := b.ready()
	for _, path := range ready {
		if b.known[path] {
			continue
		}
		b.known[path] = true
		at := b.rnd.Intn(len(b.bag) + 1)
		b.bag = append(b.bag, "")
		copy(b.bag[at+1:], b.bag[at:])
		b.bag[at] = path
	}
	if len(b.bag) == 0 {
		pool := append([]string(nil), ready...)
		b.rnd.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		b.bag = pool
		b.known = make(map[string]bool, len(pool))
		for _, path := range pool {
			b.known[path] = true
		}
	}
	if len(b.bag) == 0 {
		return "", false
	}
	last := len(b.bag) - 1
	item := b.bag[last]
	b.bag = b.bag[:last]
	return item, true
}

// FolderPlaylist — `рандомний файл -> Separator (якщо є) -> рандомний файл ->...`.
type FolderPlaylist struct {
	bag           *ShuffleBag
	separator     func() string
	wantSeparator bool
}

// NewFolderPlaylist — separator віддає "" (немає) або шлях розділювача.
func NewFolderPlaylist(ready func() []string, separator func() string, rnd *rand.Rand) *FolderPlaylist {
	return &FolderPlaylist{bag: NewShuffleBag(ready, rnd), separator: separator}
}

// Next — елемент тіла FALLBACK; false — ще нічого не готово (гравець зачекає).
func (f *FolderPlaylist) Next() (LoopItem, bool) {
	if f.wantSeparator {
		f.wantSeparator = false
		if sep := f.separator(); sep != "" {
			return LoopItem{Path: sep}, true
		}
		// separator не заданий -> одразу наступний файл
	}
	next, ok := f.bag.Next()
	if !ok {
		return LoopItem{}, false
	}
	f.wantSeparator = true
	return LoopItem{Path: next}, true
}
