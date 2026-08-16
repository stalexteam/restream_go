// Package media джейлить `media/`: лістинг, кеш тривалостей, CRUD файлів,
// слот аплоаду з валідацією. Порт controller/.
package media

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxNameLen — стеля довжини одного імені файлу/папки.
const MaxNameLen = 120

// badNameChars — керівні символи й роздільники шляху.
var badNameChars = regexp.MustCompile(`[\x00-\x1f\x7f/\\]`)

// SanitizeName — одне безпечне ім'я файлу/папки (без шляху), або "" --
// якщо такого не вийшло (порожній рядок ніколи не є валідним іменем, тож
// сентинел безпечний).
func SanitizeName(raw string) string {
	text := strings.ReplaceAll(raw, "\\", "/")
	name := badNameChars.ReplaceAllString(pathName(text), "")
	name = strings.TrimSpace(name)
	// Провідні крапки й пробіли: приховані файли й імена, що по-різному
	// виглядають у списку й на диску.
	name = strings.Trim(name, ". ")
	if name == "" || len(name) > MaxNameLen {
		return ""
	}
	return name
}

// pathName -- еквівалент python pathlib.Path(text).name: останній сегмент
// шляху ("/"-роздільник), "." відкидається, ".." лишається літерально.
func pathName(text string) string {
	var last string
	for _, part := range strings.Split(text, "/") {
		if part == "" || part == "." {
			continue
		}
		last = part
	}
	return last
}

// ResolveWithin -- справжній шлях rel усередині root, або ("", false), якщо
// веде назовні. B2: канонічний джейл, ТРИ окремі перевірки (абсолютний
// шлях, "..", симлінк назовні) -- єдине місце цієї логіки в клоні.
func ResolveWithin(root, rel string) (string, bool) {
	text := strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if strings.HasPrefix(text, "/") {
		return "", false
	}
	var parts []string
	for _, part := range strings.Split(text, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", false
		}
		parts = append(parts, part)
	}
	real, ok := resolvePath(filepath.Join(append([]string{root}, parts...)...))
	if !ok {
		return "", false
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", false // симлінк назовні
	}
	return real, true
}

// resolvePath -- Path.resolve(strict=False): абсолютний шлях із розкритими
// симлінками наявного префікса (неіснуючий хвіст лише нормалізується, як у
// python; strict=False -- дефолт, exists-перевірки немає).
func resolvePath(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rest := ""
	current := abs
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest), true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs, true
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// isDir -- os.path.isdir еквівалент, використовується лістингом/CRUD.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isFile -- os.path.isfile еквівалент.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
