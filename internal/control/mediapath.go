package control

import (
	"os"
	"path/filepath"

	"restream_go/internal/media"
)

// resolveMediaPath — шлях сегмента пресета всередині media/ або "" (веде
// назовні); джейл канонічно в internal/media.
func resolveMediaPath(mediaFile, baseDir string) string {
	real, ok := media.ResolveWithin(filepath.Join(baseDir, "media"), mediaFile)
	if !ok {
		return ""
	}
	return real
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// listFolderPaths — відеофайли теки повними шляхами, відсортовані за іменем
// (_list_folder_media віддає Path-и).
func listFolderPaths(folder string) []string {
	names := listFolderMedia(folder)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, filepath.Join(folder, name))
	}
	return out
}
