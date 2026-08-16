package fallback

import (
	"math/rand"
	"sort"
	"testing"
)

func drain(t *testing.T, bag *ShuffleBag, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		item, ok := bag.Next()
		if !ok {
			t.Fatalf("bag ran dry after %d of %d draws", i, n)
		}
		out = append(out, item)
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestShuffleBagCoversPoolEveryCycle(t *testing.T) {
	pool := []string{"a", "b", "c", "d", "e"}
	bag := NewShuffleBag(func() []string { return pool }, rand.New(rand.NewSource(7)))
	for cycle := 0; cycle < 4; cycle++ {
		drawn := drain(t, bag, len(pool))
		if !equal(sorted(drawn), pool) {
			t.Fatalf("cycle %d drew %v, want every file exactly once", cycle, drawn)
		}
	}
}

func TestShuffleBagReshufflesBetweenCycles(t *testing.T) {
	pool := []string{"a", "b", "c", "d", "e", "f"}
	bag := NewShuffleBag(func() []string { return pool }, rand.New(rand.NewSource(11)))
	same := 0
	first := drain(t, bag, len(pool))
	for cycle := 0; cycle < 8; cycle++ {
		if equal(drain(t, bag, len(pool)), first) {
			same++
		}
	}
	if same > 1 {
		t.Fatalf("%d of 8 cycles repeated the same order -- the pool is not reshuffled", same)
	}
}

func TestShuffleBagMixesNewFilesIntoTheRemainder(t *testing.T) {
	ready := []string{"a", "b", "c"}
	bag := NewShuffleBag(func() []string { return ready }, rand.New(rand.NewSource(3)))
	first, _ := bag.Next()

	ready = append(ready, "d", "e", "f")
	rest := drain(t, bag, 5)
	seen := map[string]bool{first: true}
	for _, item := range rest {
		if seen[item] {
			t.Fatalf("%q repeated before the cycle ended: %v after %q", item, rest, first)
		}
		seen[item] = true
	}
	if len(seen) != len(ready) {
		t.Fatalf("saw %d of %d files in the cycle", len(seen), len(ready))
	}
}

func TestShuffleBagEmptyPool(t *testing.T) {
	bag := NewShuffleBag(func() []string { return nil }, rand.New(rand.NewSource(1)))
	if item, ok := bag.Next(); ok {
		t.Fatalf("empty pool yielded %q", item)
	}
}

func TestShuffleBagIsDeterministicPerSeed(t *testing.T) {
	pool := []string{"a", "b", "c", "d"}
	draw := func() []string {
		bag := NewShuffleBag(func() []string { return pool }, rand.New(rand.NewSource(42)))
		return drain(t, bag, 8)
	}
	if !equal(draw(), draw()) {
		t.Fatal("the same seed produced different sequences")
	}
}

func TestFolderPlaylistAlternatesWithSeparator(t *testing.T) {
	pool := []string{"a", "b", "c"}
	list := NewFolderPlaylist(func() []string { return pool },
		func() string { return "sep" }, rand.New(rand.NewSource(5)))
	for i := 0; i < 6; i++ {
		item, ok := list.Next()
		if !ok {
			t.Fatalf("item %d missing", i)
		}
		if item.Loop {
			t.Fatalf("folder items are played once, got loop for %q", item.Path)
		}
		if i%2 == 1 && item.Path != "sep" {
			t.Fatalf("item %d is %q, want the separator", i, item.Path)
		}
		if i%2 == 0 && item.Path == "sep" {
			t.Fatalf("item %d is the separator, want a file", i)
		}
	}
}

func TestFolderPlaylistWithoutSeparatorPlaysFilesBackToBack(t *testing.T) {
	pool := []string{"a", "b", "c"}
	list := NewFolderPlaylist(func() []string { return pool },
		func() string { return "" }, rand.New(rand.NewSource(5)))
	for i := 0; i < 6; i++ {
		item, ok := list.Next()
		if !ok || item.Path == "" {
			t.Fatalf("item %d: %+v ok=%v", i, item, ok)
		}
	}
}

func TestFolderPlaylistWaitsWhenNothingIsReady(t *testing.T) {
	var pool []string
	list := NewFolderPlaylist(func() []string { return pool },
		func() string { return "sep" }, rand.New(rand.NewSource(5)))
	if _, ok := list.Next(); ok {
		t.Fatal("nothing is prepared yet -- the player must wait")
	}
	pool = []string{"a"}
	if item, ok := list.Next(); !ok || item.Path != "a" {
		t.Fatalf("got %+v ok=%v after the first file became ready", item, ok)
	}
}

// Новий файл іде у ВИПАДКОВУ позицію решти мішка, не в фіксований край.
func TestShuffleBagInsertsNewFilesAtRandomPositions(t *testing.T) {
	positions := map[int]bool{}
	for seed := int64(0); seed < 50; seed++ {
		pool := []string{"a", "b", "c", "d"}
		bag := NewShuffleBag(func() []string { return pool }, rand.New(rand.NewSource(seed)))
		if _, ok := bag.Next(); !ok {
			t.Fatal("the bag is empty on the first draw")
		}
		pool = append(pool, "new")
		for i, item := range drain(t, bag, 4) {
			if item == "new" {
				positions[i] = true
			}
		}
	}
	if len(positions) < 3 {
		t.Fatalf("the new file only ever landed at positions %v", positions)
	}
}
