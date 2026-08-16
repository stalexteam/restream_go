package media

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"restream_go/internal/probe"
)

// AcquireUploadSlot -- аплоад строго один за раз.
func (s *Store) AcquireUploadSlot() bool {
	select {
	case <-s.uploadSlot:
		return true
	default:
		return false
	}
}

// ReleaseUploadSlot -- безпечно кликати навіть без утриманого слоту.
func (s *Store) ReleaseUploadSlot() {
	select {
	case s.uploadSlot <- struct{}{}:
	default:
	}
}

// PrepareUpload -- (part, final) шляхи для нового файлу; errMsg != "" --
// відмова, part/final порожні.
func (s *Store) PrepareUpload(relDir, rawName string, size int64) (part, final, errMsg string) {
	parent, ok := s.Resolve(relDir)
	if !ok || !isDir(parent) {
		return "", "", "target folder not found"
	}
	name := SanitizeName(rawName)
	if name == "" {
		return "", "", "invalid file name"
	}
	if !videoExts[strings.ToLower(filepath.Ext(name))] {
		return "", "", "only video files are accepted (mp4 / mkv / mov / …)"
	}
	finalPath := filepath.Join(parent, name)
	if exists(finalPath) {
		return "", "", fmt.Sprintf("'%s' already exists -- rename or delete it first", name)
	}
	if free, ok := diskFree(s.root); ok && free < uint64(size)+uint64(FreeSpaceMarginBytes) {
		return "", "", fmt.Sprintf(
			"not enough free space: %s needed plus a %s margin, %s available",
			gb(size), gb(FreeSpaceMarginBytes), gb(int64(free)))
	}
	partPath := filepath.Join(parent, fmt.Sprintf(".%s.part", name))
	return partPath, finalPath, ""
}

// RejectUnusableMedia -- причина, чому файл не годиться в заглушку, або ""
// (F10: відео+аудіо обов'язкові в кожному файлі).
func RejectUnusableMedia(path string) string {
	tracks, ok := probe.ProbeMediaTracks(path)
	var reason string
	switch {
	case !ok:
		reason = "unreadable (ffprobe could not open it)"
	case !tracks.Video:
		reason = "no video track"
	case !tracks.Audio:
		reason = "no audio track"
	default:
		return ""
	}
	return fmt.Sprintf("rejected -- %s; fallback material needs both a video and an audio track", reason)
}

// FinalizeUpload -- валідує і перейменовує part у final; storeFailed
// відрізняє збій rename від відмови валідації.
func (s *Store) FinalizeUpload(part, final string) (rel, errMsg string, storeFailed bool) {
	if reason := RejectUnusableMedia(part); reason != "" {
		_ = os.Remove(part)
		return "", reason, false
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return "", fmt.Sprintf("could not store the file: %v", err), true
	}
	rel, _ = s.Relative(final)
	return rel, "", false
}

func gb(sizeBytes int64) string {
	return fmt.Sprintf("%.1f GB", float64(sizeBytes)/(1024*1024*1024))
}
