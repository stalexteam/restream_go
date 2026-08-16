package media

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry -- один рядок лістингу теки (папка або відеофайл).
type Entry struct {
	Name       string   `json:"name"`
	IsDir      bool     `json:"is_dir"`
	Size       *int64   `json:"size"`
	Duration   *float64 `json:"duration"`
	VideoCount *int     `json:"video_count"`
}

// Listing -- {path, entries} однієї теки.
type Listing struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

// FileStat -- розмір + (кешована) тривалість одного файлу.
type FileStat struct {
	Path     string   `json:"path"`
	Size     int64    `json:"size"`
	Duration *float64 `json:"duration"`
}

// SuggestEntry -- рядок автодоповнення поля шляху.
type SuggestEntry struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	VideoCount *int   `json:"video_count"`
}

// SuggestStatus -- стан уже введеного (повного) значення поля.
type SuggestStatus struct {
	Exists     bool `json:"exists"`
	IsDir      bool `json:"is_dir"`
	VideoCount int  `json:"video_count"`
}

// Suggestion -- підказки + стан, одним запитом.
type Suggestion struct {
	Dir     string         `json:"dir"`
	Entries []SuggestEntry `json:"entries"`
	Status  SuggestStatus  `json:"status"`
}

// RangedFile -- шлях/розмір/content-type для стрімінгу превʼю.
type RangedFile struct {
	Path        string
	Size        int64
	ContentType string
}

// ListDir -- лише підтеки й відеофайли (менеджер керує матеріалом заглушки,
// не всім диском); теки йдуть перед файлами, обидві групи -- за іменем без
// урахування регістру.
func (s *Store) ListDir(rel string) (Listing, bool) {
	target, ok := s.Resolve(rel)
	if !ok || !isDir(target) {
		return Listing{}, false
	}
	pathRel, ok := s.Relative(target)
	if !ok {
		return Listing{}, false
	}

	children, err := os.ReadDir(target)
	if err != nil {
		log.Printf("could not list '%s': %v", target, err)
		return Listing{}, false
	}
	sort.SliceStable(children, func(i, j int) bool {
		return strings.ToLower(children[i].Name()) < strings.ToLower(children[j].Name())
	})

	var dirs, files []Entry
	var unknown []string
	for _, child := range children {
		full := filepath.Join(target, child.Name())
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if info.IsDir() {
			count := s.CountVideos(full)
			dirs = append(dirs, Entry{Name: child.Name(), IsDir: true, VideoCount: &count})
			continue
		}
		ext := strings.ToLower(filepath.Ext(child.Name()))
		if !videoExts[ext] {
			continue
		}
		size := info.Size()
		known, duration := s.cachedDuration(full, info)
		if !known {
			unknown = append(unknown, full)
		}
		files = append(files, Entry{Name: child.Name(), IsDir: false, Size: &size, Duration: duration})
	}

	s.scheduleDurations(pathRel, unknown)
	entries := append(dirs, files...)
	if entries == nil {
		entries = []Entry{}
	}
	return Listing{Path: pathRel, Entries: entries}, true
}

// CountVideos -- кількість відеофайлів безпосередньо в folder (не рекурсивно).
func (s *Store) CountVideos(folder string) int {
	children, err := os.ReadDir(folder)
	if err != nil {
		return 0
	}
	count := 0
	for _, child := range children {
		if !videoExts[strings.ToLower(filepath.Ext(child.Name()))] {
			continue
		}
		info, err := os.Stat(filepath.Join(folder, child.Name()))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		count++
	}
	return count
}

// StatFile -- розмір + (кешована) тривалість одного файлу, для превʼю й підказок.
func (s *Store) StatFile(rel string) (FileStat, bool) {
	target, ok := s.Resolve(rel)
	if !ok {
		return FileStat{}, false
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return FileStat{}, false
	}
	_, duration := s.cachedDuration(target, info)
	path, _ := s.Relative(target)
	return FileStat{Path: path, Size: info.Size(), Duration: duration}, true
}

// Suggest -- підказки для поля шляху + стан введеного значення, одним запитом.
// Придатність файлу (відео+аудіо) тут не міряється -- це ffprobe на кожну
// літеру, її робить Save.
func (s *Store) Suggest(prefix string, dirsOnly bool, limit int) (Suggestion, bool) {
	text := strings.ReplaceAll(prefix, "\\", "/")
	head, partial := rpartition(text, "/")
	listing, ok := s.ListDir(head)
	if !ok {
		// Без теки-батька status усе одно потрібен полю.
		if _, inside := s.Resolve(strings.TrimRight(text, "/")); !inside {
			return Suggestion{}, false
		}
		return Suggestion{Entries: []SuggestEntry{}}, true
	}

	needle := strings.ToLower(partial)
	var entries []SuggestEntry
	for _, entry := range listing.Entries {
		if !(entry.IsDir || !dirsOnly) {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(entry.Name), needle) {
			continue
		}
		entries = append(entries, SuggestEntry{Name: entry.Name, IsDir: entry.IsDir, VideoCount: entry.VideoCount})
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	if entries == nil {
		entries = []SuggestEntry{}
	}

	status := SuggestStatus{}
	if target, ok := s.Resolve(strings.TrimRight(text, "/")); ok {
		if info, err := os.Stat(target); err == nil {
			status.Exists = true
			status.IsDir = info.IsDir()
			if status.IsDir {
				status.VideoCount = s.CountVideos(target)
			}
		}
	}
	return Suggestion{Dir: listing.Path, Entries: entries, Status: status}, true
}

// OpenRanged -- шлях/розмір/content-type для стрімінгу превʼю.
func (s *Store) OpenRanged(rel string) (RangedFile, bool) {
	target, ok := s.Resolve(rel)
	if !ok {
		return RangedFile{}, false
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return RangedFile{}, false
	}
	ext := strings.ToLower(filepath.Ext(target))
	if !videoExts[ext] {
		return RangedFile{}, false
	}
	ct, known := contentTypes[ext]
	if !known {
		ct = "application/octet-stream"
	}
	return RangedFile{Path: target, Size: info.Size(), ContentType: ct}, true
}

// rpartition -- python str.rpartition: розбиває по ОСТАННЬОМУ входженню sep;
// без збігу -- ("", text), не (text, "").
func rpartition(text, sep string) (head, tail string) {
	i := strings.LastIndex(text, sep)
	if i < 0 {
		return "", text
	}
	return text[:i], text[i+len(sep):]
}

// cachedDuration -- (чи вже міряли, тривалість). Невдалий probe теж «міряли».
func (s *Store) cachedDuration(path string, info os.FileInfo) (bool, *float64) {
	key := durationKeyFor(path, info)
	s.mu.Lock()
	defer s.mu.Unlock()
	v, known := s.durations[key]
	return known, v
}

func durationKeyFor(path string, info os.FileInfo) durationKey {
	return durationKey{path: path, mtimeNs: info.ModTime().UnixNano(), size: info.Size()}
}

func (s *Store) scheduleDurations(pathRel string, paths []string) {
	if len(paths) == 0 {
		return
	}
	s.mu.Lock()
	if s.probing[pathRel] {
		s.mu.Unlock()
		return
	}
	s.probing[pathRel] = true
	s.mu.Unlock()
	go s.fillDurations(pathRel, paths)
}

// fillDurations -- фонова горутина: python-потік ковтає власні винятки
// мовчки, тут panic убив би процес, тож recover тут ширший, ніж в оригіналі.
func (s *Store) fillDurations(pathRel string, paths []string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("media: durations probe panicked for '%s': %v", pathRel, r)
		}
		s.mu.Lock()
		delete(s.probing, pathRel)
		s.mu.Unlock()
		s.notifyDurationsReady(pathRel)
	}()
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		key := durationKeyFor(path, info)
		s.mu.Lock()
		_, known := s.durations[key]
		s.mu.Unlock()
		if known {
			continue
		}
		value, ok := s.probeDuration(path)
		s.mu.Lock()
		if ok {
			v := value
			s.durations[key] = &v
		} else {
			s.durations[key] = nil
		}
		s.mu.Unlock()
	}
}

func (s *Store) notifyDurationsReady(pathRel string) {
	if s.OnDurationsReady == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("media_store: durations callback failed: %v", r)
		}
	}()
	s.OnDurationsReady(pathRel)
}
