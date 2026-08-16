package control

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// listFolderMedia — лише відеофайли теки, за іменем (byte-wise); підкаталоги
// й чужі розширення не рахуються, відсутня тека — порожньо.
func TestListFolderMedia(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.MP4", "a.mkv", "A.mp4", "c.txt", "z.mov"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	want := []string{"A.mp4", "a.mkv", "b.MP4", "z.mov"}
	if got := listFolderMedia(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("listFolderMedia = %v, want %v", got, want)
	}
	if got := listFolderMedia(filepath.Join(dir, "does_not_exist")); len(got) != 0 {
		t.Errorf("listFolderMedia(missing) = %v, want nothing", got)
	}
}
