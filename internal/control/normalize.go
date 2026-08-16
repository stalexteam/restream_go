package control

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"restream_go/internal/proc"
	"restream_go/internal/wire/flv"
)

// Типи потоків/виходів (,63).
var (
	sourceTypes   = map[string]bool{"rtmp": true, "srt": true}
	platformTypes = map[string]bool{"rtmp": true, "srt": true}
)

// MaxEBVideoTracks — стеля сходинок EB-драбини на один канвас.
const MaxEBVideoTracks = 5

const (
	defaultPresetID = "default"
	defaultGroupID  = "default"
	defaultLoopFile = "backup.mp4"
)

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".mov": true, ".flv": true, ".avi": true,
	".ts": true, ".m4v": true, ".webm": true, ".mpg": true, ".mpeg": true,
}

// SafeProcName — ім'я для файлу лога ffmpeg-<name>.log; джерело правди —
// proc.SafeName (його ж кличе platform.NewSpec).
func SafeProcName(name string) string { return proc.SafeName(name) }

// SourceTrackCount — кількість аудіодоріжок source: rtmp=1 (2 з
// vod_track), srt=декларована (1..6).
func SourceTrackCount(scfg *Dict) int {
	stype, _ := scfg.Get("type")
	if s, ok := stype.(string); ok && s == "srt" {
		n, ok := pyInt(scfg.GetOr("audio_tracks", int64(1)))
		if !ok {
			n = 1
		}
		if n < 1 {
			n = 1
		}
		if n > 6 {
			n = 6
		}
		return int(n)
	}
	if s, ok := stype.(string); ok && s == "rtmp" {
		vt, _ := scfg.Get("vod_track")
		if pyTruthy(vt) {
			return 2
		}
	}
	return 1
}

// SourceVideoTracks — декларація кількості відеотреків EB-драбини: 1..
// MaxEBVideoTracks, 0=AUTO; поза EB завжди 0.
func SourceVideoTracks(scfg *Dict) int {
	stype, _ := scfg.Get("type")
	eb, _ := scfg.Get("enhanced_broadcasting")
	s, ok := stype.(string)
	if !(ok && s == "rtmp" && pyTruthy(eb)) {
		return 0
	}
	n, ok := pyInt(scfg.GetOr("video_tracks", int64(0)))
	if !ok {
		return 0
	}
	if n >= 1 && n <= MaxEBVideoTracks {
		return int(n)
	}
	return 0
}

// NormalizeSources — список source-ів; порожній/відсутній -> один
// дефолтний. Рівно один is_default, валідний тип/лічильник доріжок.
func NormalizeSources(config *Dict) []*Dict {
	var sources []*Dict
	if raw, ok := listOf(config, "sources"); ok {
		for _, item := range raw {
			if s, ok := item.(*Dict); ok {
				if name, _ := s.Get("name"); pyTruthy(name) {
					sources = append(sources, s)
				}
			}
		}
	}
	if len(sources) == 0 {
		return []*Dict{D(
			"name", "Main", "is_default", true, "type", "rtmp", "vod_track", false,
			"enhanced_broadcasting", false, "video_tracks", int64(0),
			"live_path", "live/main", "audio_tracks", int64(1),
		)}
	}
	anyDefault := false
	for _, s := range sources {
		if v, _ := s.Get("is_default"); pyTruthy(v) {
			anyDefault = true
			break
		}
	}
	if !anyDefault {
		sources[0].Set("is_default", true)
	}
	for _, s := range sources {
		t, _ := s.Get("type")
		ts, ok := t.(string)
		if !ok || !sourceTypes[ts] {
			ts = "rtmp"
			s.Set("type", ts)
		}
		if ts == "rtmp" {
			vt, _ := s.Get("vod_track")
			s.Set("vod_track", pyTruthy(vt))
			eb, _ := s.Get("enhanced_broadcasting")
			s.Set("enhanced_broadcasting", pyTruthy(eb))
		} else {
			s.Set("vod_track", false)
			s.Set("enhanced_broadcasting", false)
		}
		s.Set("video_tracks", int64(SourceVideoTracks(s)))
		s.Set("audio_tracks", int64(SourceTrackCount(s)))
	}
	return sources
}

// NormalizeGroups — список груп платформ; гарантує рівно одну дефолтну
// (незнищенну).
func NormalizeGroups(config *Dict) []*Dict {
	var groups []*Dict
	if raw, ok := listOf(config, "platform_groups"); ok {
		for _, item := range raw {
			if g, ok := item.(*Dict); ok {
				if id, _ := g.Get("id"); pyTruthy(id) {
					groups = append(groups, g)
				}
			}
		}
	}
	defaultSeen := false
	taken := map[string]bool{}
	for _, g := range groups {
		id, _ := g.Get("id")
		g.SetDefault("name", id)
		// Дублікат id робить CRUD-по-id недетермінованим.
		if taken[pyStr(id)] {
			id = slug(pyStr(g.GetOr("name", id)), taken, "group")
			g.Set("id", id)
		}
		taken[pyStr(id)] = true
		g.Set("enabled", pyTruthy(g.GetOr("enabled", true)))
		isDefault, _ := g.Get("is_default")
		if pyTruthy(isDefault) && !defaultSeen {
			defaultSeen = true
			g.Set("is_default", true) // у персист іде bool, а не сире truthy (Q55)
		} else {
			g.Set("is_default", false)
		}
	}
	if len(groups) == 0 {
		return []*Dict{D("id", defaultGroupID, "name", "Default", "is_default", true, "enabled", true)}
	}
	if !defaultSeen {
		groups[0].Set("is_default", true)
	}
	return groups
}

// clampAudioMap — audio_map: слот i несе джерельну доріжку 0..
// MaxAudioSlots-1 або nil.
func clampAudioMap(raw any) []any {
	var result []any
	if list, ok := raw.([]any); ok {
		n := len(list)
		if n > flv.MaxAudioSlots {
			n = flv.MaxAudioSlots
		}
		for _, v := range list[:n] {
			iv, ok := pyInt(v)
			var val any
			if ok && iv >= 0 && iv < flv.MaxAudioSlots {
				val = iv
			}
			result = append(result, val)
		}
	}
	if len(result) == 0 {
		result = []any{int64(0)}
	}
	for len(result) < flv.MaxAudioSlots {
		result = append(result, nil)
	}
	return result
}

// NormalizePlatforms — список платформ (плаский, може бути порожній).
func NormalizePlatforms(config *Dict) []*Dict {
	var platforms []*Dict
	if raw, ok := listOf(config, "platforms"); ok {
		for _, item := range raw {
			if p, ok := item.(*Dict); ok {
				if name, _ := p.Get("name"); pyTruthy(name) {
					platforms = append(platforms, p)
				}
			}
		}
	}
	for _, p := range platforms {
		t, _ := p.Get("type")
		ts, ok := t.(string)
		if !ok || !platformTypes[ts] {
			ts = "rtmp"
			p.Set("type", ts)
		}
		if ts == "rtmp" {
			vt, _ := p.Get("vod_track")
			p.Set("vod_track", pyTruthy(vt))
		} else {
			p.Set("vod_track", false)
		}
		p.Set("enabled", pyTruthy(p.GetOr("enabled", false)))
		for _, field := range []string{"group", "source", "server", "key", "streamid", "passphrase", "backup_preset"} {
			p.SetDefault(field, "")
		}
		audio, ok := pyInt(p.GetOr("audio", int64(0)))
		if !ok {
			audio = 0
		}
		p.Set("audio", maxInt64(audio, 0))
		audioVod, ok := pyInt(p.GetOr("audio_vod", int64(1)))
		if !ok {
			audioVod = 1
		}
		p.Set("audio_vod", maxInt64(audioVod, 0))
		video, ok := pyInt(p.GetOr("video", int64(0)))
		if !ok {
			video = 0
		}
		if video < 0 {
			if ts == "rtmp" {
				video = -1
			} else {
				video = 0
			}
		}
		p.Set("video", video)
		if ts == "srt" {
			am, _ := p.Get("audio_map")
			p.Set("audio_map", clampAudioMap(am))
		} else {
			p.Pop("audio_map")
		}
	}
	return platforms
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// slug — [^A-Za-z0-9_-]+ -> "-", strip("-"), lower, суфікси -2/-3 на
// колізіях.
func slug(name string, taken map[string]bool, fallback string) string {
	base := slugRe.ReplaceAllString(name, "-")
	base = strings.Trim(base, "-")
	base = strings.ToLower(base)
	if base == "" {
		base = fallback
	}
	candidate := base
	i := 2
	for taken[candidate] {
		candidate = base + "-" + strconv.Itoa(i)
		i++
	}
	return candidate
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// NormalizePresets — список fallback-пресетів; стабільний id (slug з
// імені), валідний type, рівно один is_default, хоч один пресет.
func NormalizePresets(config *Dict) []*Dict {
	var presets []*Dict
	if raw, ok := listOf(config, "fallback_presets"); ok {
		for _, item := range raw {
			if p, ok := item.(*Dict); ok {
				presets = append(presets, p)
			}
		}
	}

	taken := map[string]bool{}
	for _, p := range presets {
		idVal, _ := p.Get("id")
		pid := strings.TrimSpace(pyStr(pyOr(idVal, "")))
		if pid == "" {
			nameVal, _ := p.Get("name")
			pid = slug(pyStr(nameVal), taken, "preset")
		}
		for taken[pid] {
			nameVal, _ := p.Get("name")
			pid = slug(pyStr(pyOr(nameVal, "preset")), taken, "preset")
		}
		p.Set("id", pid)
		taken[pid] = true
		t, _ := p.Get("type")
		ts, ok := t.(string)
		if !ok || (ts != "sequence" && ts != "folder") {
			p.Set("type", "sequence")
		}
		for _, field := range []string{"start_file", "loop_file", "end_file", "folder", "separator_file"} {
			p.SetDefault(field, "")
		}
		p.SetDefault("name", pid)
	}

	if len(presets) == 0 {
		presets = []*Dict{D(
			"id", defaultPresetID, "name", "Default", "is_default", true,
			"type", "sequence", "start_file", "",
			"loop_file", defaultLoopFile, "end_file", "",
			"folder", "", "separator_file", "",
		)}
	}

	defaultSeen := false
	for _, p := range presets {
		isDefault, _ := p.Get("is_default")
		if pyTruthy(isDefault) && !defaultSeen {
			defaultSeen = true
			p.Set("is_default", true)
		} else {
			p.Set("is_default", false)
		}
	}
	if !defaultSeen {
		presets[0].Set("is_default", true)
	}

	for _, p := range presets {
		isDefault, _ := p.Get("is_default")
		ptype := pyStr(p.GetOr("type", "sequence"))
		loopFile := strings.TrimSpace(pyStr(pyOr(p.GetOr("loop_file", nil), "")))
		if pyTruthy(isDefault) && ptype == "sequence" && loopFile == "" {
			p.Set("loop_file", defaultLoopFile)
		}
	}
	return presets
}

// listOf — config.get(key), якщо це list; інакше (nil, false).
func listOf(config *Dict, key string) ([]any, bool) {
	v, _ := config.Get(key)
	list, ok := v.([]any)
	return list, ok
}

// listFolderMedia — відео-файли теки, відсортовані за іменем; OSError
// (в т.ч. відсутня тека) -> порожній список.
func listFolderMedia(folder string) []string {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		info, err := os.Stat(filepath.Join(folder, e.Name()))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
