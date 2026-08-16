package settings

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"restream_go/internal/media"
	"restream_go/internal/probe"
)

var presetVideoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".mov": true, ".flv": true, ".avi": true,
	".ts": true, ".m4v": true, ".webm": true, ".mpg": true, ".mpeg": true,
}

// PresetFiles — сирі поля fallback-пресета, як їх будує http_server._preset_files.
type PresetFiles struct {
	Type          string
	LoopFile      string
	StartFile     string
	EndFile       string
	Folder        string
	SeparatorFile string
}

// BackupRoot — корінь матеріалу заглушок; у клоні це media/.
func BackupRoot(baseDir string) string {
	return filepath.Join(baseDir, "media")
}

// ResolveMediaPath — шлях сегмента пресета всередині media/, або "" якщо
// веде за межі. Джейл — канонічний media.ResolveWithin (B2, SS3 злито).
func ResolveMediaPath(mediaFile, baseDir string) string {
	resolved, ok := media.ResolveWithin(BackupRoot(baseDir), mediaFile)
	if !ok {
		return ""
	}
	return resolved
}

// ValidateMediaFile — причина, чому файл не годиться для сегмента заглушки,
// або (,false) якщо ок.
func ValidateMediaFile(mediaFile, baseDir string) (string, bool) {
	resolved := ResolveMediaPath(mediaFile, baseDir)
	if resolved == "" {
		return "path leaves the backup folder", true
	}
	if !isFile(resolved) {
		return "file not found", true
	}
	tracks, ok := probe.ProbeMediaTracks(resolved)
	if !ok {
		return "unreadable (ffprobe failed)", true
	}
	if !tracks.Video {
		return "no video track", true
	}
	if !tracks.Audio {
		return "no audio track", true
	}
	return "", false
}

type namedField struct {
	key   string
	value string
}

// ValidatePreset — валідація одного fallback-пресета (add/update);
// all-or-nothing, обривається на першому поганому файлі.
func ValidatePreset(name string, files PresetFiles, existingNames []string, baseDir string) map[string]string {
	errors := map[string]string{}
	clean := strings.TrimSpace(name)
	if clean == "" {
		errors["name"] = "name is required"
	} else if containsStr(existingNames, clean) {
		errors["name"] = fmt.Sprintf("a fallback preset named '%s' already exists", clean)
	}

	ptype := files.Type
	if ptype != "sequence" && ptype != "folder" {
		ptype = "sequence"
	}

	var optional []namedField
	if ptype == "folder" {
		folder := strings.TrimSpace(files.Folder)
		if folder == "" {
			errors["folder"] = "folder is required"
			return errors
		}
		resolved := ResolveMediaPath(folder, baseDir)
		if resolved == "" {
			errors["folder"] = "path leaves the backup folder"
			return errors
		}
		if !isDir(resolved) {
			errors["folder"] = fmt.Sprintf("folder not found: backup/%s", strings.Trim(folder, "/"))
			return errors
		}
		if !folderHasVideo(resolved) {
			errors["folder"] = "folder has no video files"
			return errors
		}
		optional = []namedField{
			{"separator_file", files.SeparatorFile}, {"start_file", files.StartFile}, {"end_file", files.EndFile},
		}
	} else {
		loopFile := strings.TrimSpace(files.LoopFile)
		if loopFile == "" {
			errors["loop_file"] = "loop video is required"
			return errors
		}
		optional = []namedField{
			{"loop_file", files.LoopFile}, {"start_file", files.StartFile}, {"end_file", files.EndFile},
		}
	}

	for _, f := range optional {
		value := strings.TrimSpace(f.value)
		if value == "" {
			continue
		}
		reason, bad := ValidateMediaFile(value, baseDir)
		if bad {
			errors[f.key] = fmt.Sprintf("%s: %s", path.Base(value), reason)
			return errors
		}
	}
	return errors
}

func folderHasVideo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if presetVideoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			return true
		}
	}
	return false
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
