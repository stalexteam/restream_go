package media

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CreateDir -- нова тека всередині relDir; "" -- успіх, інакше причина.
func (s *Store) CreateDir(relDir, rawName string) string {
	parent, ok := s.Resolve(relDir)
	if !ok || !isDir(parent) {
		return "folder not found"
	}
	name := SanitizeName(rawName)
	if name == "" {
		return "invalid folder name"
	}
	target := filepath.Join(parent, name)
	if exists(target) {
		return fmt.Sprintf("'%s' already exists", name)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return fmt.Sprintf("could not create the folder: %v", err)
	}
	return ""
}

// Rename -- нове ім'я для файлу/теки за rel; "" -- успіх.
func (s *Store) Rename(rel, rawName string) string {
	target, ok := s.Resolve(rel)
	if !ok || target == s.root {
		return "not found"
	}
	if !exists(target) {
		return "not found"
	}
	name := SanitizeName(rawName)
	if name == "" {
		return "invalid name"
	}
	if isFile(target) && !videoExts[strings.ToLower(filepath.Ext(name))] {
		return "keep a video extension (mp4 / mkv / mov / …)"
	}
	destination := filepath.Join(filepath.Dir(target), name)
	if destination == target {
		return ""
	}
	if exists(destination) {
		return fmt.Sprintf("'%s' already exists", name)
	}
	if err := os.Rename(target, destination); err != nil {
		return fmt.Sprintf("could not rename: %v", err)
	}
	return ""
}

// Delete -- файл або тека (рекурсивно) за rel; "" -- успіх.
func (s *Store) Delete(rel string) string {
	target, ok := s.Resolve(rel)
	if !ok || target == s.root {
		return "cannot delete the backup folder itself"
	}
	if !exists(target) {
		return "not found"
	}
	var err error
	if isDir(target) {
		err = os.RemoveAll(target)
	} else {
		err = os.Remove(target)
	}
	if err != nil {
		return fmt.Sprintf("could not delete: %v", err)
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
