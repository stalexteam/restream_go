package media

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"restream_go/internal/probe"
)

// videoExts -- відеорозширення, які менеджер керує в media/ (ротація й
// пресети, не весь диск).
var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".mov": true, ".flv": true, ".avi": true,
	".ts": true, ".m4v": true, ".webm": true, ".mpg": true, ".mpeg": true,
}

// contentTypes -- Content-Type для превʼю в браузері за розширенням.
var contentTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm",
	".mov": "video/quicktime", ".mkv": "video/x-matroska", ".ts": "video/mp2t",
	".flv": "video/x-flv", ".avi": "video/x-msvideo",
	".mpg": "video/mpeg", ".mpeg": "video/mpeg",
}

// FreeSpaceMarginBytes -- запас понад розмір аплоаду: файл ще нормалізується
// в tmp/backup-cache/, по копії на кожні цільові параметри.
const FreeSpaceMarginBytes int64 = 512 * 1024 * 1024

// durationKey -- (шлях, mtime, розмір): перезаписаний файл дає інший ключ,
// протухлого значення в кеші тому не буває.
type durationKey struct {
	path    string
	mtimeNs int64
	size    int64
}

// Store -- джейл media/: лістинг, кеш тривалостей, CRUD, слот аплоаду.
type Store struct {
	root string

	mu        sync.Mutex
	durations map[durationKey]*float64
	probing   map[string]bool

	// uploadSlot -- буферизований канал-семафор: токен присутній -- слот вільний.
	uploadSlot chan struct{}

	// OnDurationsReady кличеться з фонової горутини, коли для теки добрано
	// тривалості. nil -- нікого не сповіщати.
	OnDurationsReady func(pathRel string)

	// probeDuration -- шов для тестів; прод не підміняє.
	probeDuration func(path string) (float64, bool)
}

// NewStore створює (якщо немає) і джейлить root.
func NewStore(root string) *Store {
	_ = os.MkdirAll(root, 0o755)
	resolved, ok := resolvePath(root)
	if !ok {
		resolved = root
	}
	s := &Store{
		root:          resolved,
		durations:     make(map[durationKey]*float64),
		probing:       make(map[string]bool),
		uploadSlot:    make(chan struct{}, 1),
		probeDuration: probe.ProbeDurationSec,
	}
	s.uploadSlot <- struct{}{}
	return s
}

// Root -- джейлений корінь (для параметрів, повʼязаних із root-каталогом).
func (s *Store) Root() string { return s.root }

// Resolve -- rel всередині кореня, або ("", false).
func (s *Store) Resolve(rel string) (string, bool) {
	return ResolveWithin(s.root, rel)
}

// Relative -- шлях відносно кореня у форматі "/"; "" для самого кореня.
// ("", false) -- шлях не всередині кореня.
func (s *Store) Relative(path string) (string, bool) {
	real, ok := resolvePath(path)
	if !ok {
		return "", false
	}
	if real == s.root {
		return "", true
	}
	if !strings.HasPrefix(real, s.root+string(filepath.Separator)) {
		return "", false
	}
	rel := strings.TrimPrefix(real, s.root+string(filepath.Separator))
	return filepath.ToSlash(rel), true
}

// ParentOf -- батьківський rel-шлях ("" для кореня чи файлу в корені).
func ParentOf(rel string) string {
	text := strings.Trim(strings.ReplaceAll(rel, "\\", "/"), "/")
	i := strings.LastIndex(text, "/")
	if i < 0 {
		return ""
	}
	return text[:i]
}
