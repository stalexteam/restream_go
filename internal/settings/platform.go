package settings

import (
	"fmt"
	"strings"

	"restream_go/internal/egress"
)

var platformTypes = map[string]bool{"rtmp": true, "srt": true}

// SourceInfo — вузький зріз source-а для валідації платформи.
type SourceInfo struct {
	Type                 string
	AudioTracks          int
	EnhancedBroadcasting bool
	VideoTracks          int
}

// ValidatePlatform — валідація однієї платформи (add/update).
// streamid/passphrase тілом функції не читаються --
// мертві параметри.
func ValidatePlatform(name, ptype string, vodTrack bool, server, key, streamid, passphrase, source string,
	audio, audioVod, audioMap any, backupPreset string, existingNames []string,
	sourcesInfo map[string]SourceInfo, knownPresetIDs []string, video any) map[string]string {

	errors := validateName(name, existingNames, "platform")
	if !platformTypes[ptype] {
		errors["type"] = "unknown platform type"
		return errors
	}

	src, found := sourcesInfo[strings.TrimSpace(source)]
	var srcPtr *SourceInfo
	if found {
		srcPtr = &src
	}
	videoN, videoOK := pyIntStrict(video)
	passthrough := videoOK && videoN < 0

	switch {
	case ptype == "srt":
		if !strings.HasPrefix(strings.TrimSpace(server), "srt://") {
			errors["server"] = "server must be an srt:// URL"
		}
	case passthrough:
		if strings.TrimSpace(key) == "" {
			errors["key"] = "an Enhanced Broadcasting platform needs the channel stream key"
		}
	default:
		if !isRTMP(egress.BuildPushURL(server, key)) {
			errors["server"] = "server must be an rtmp:// or rtmps:// URL"
		}
	}

	for k, v := range validateVideoPick(video, ptype, srcPtr) {
		errors[k] = v
	}

	switch {
	case !found:
		errors["source"] = "unknown source"
	case ptype == "srt":
		tracks := src.AudioTracks
		list, isList := audioMap.([]any)
		if !isList || len(list) == 0 {
			errors["audio_map"] = "at least one track mapping is required"
			break
		}
		limit := len(list)
		if limit > MaxSRTAudioTracks {
			limit = MaxSRTAudioTracks
		}
		valid, anyMapped := true, false
		for _, v := range list[:limit] {
			if v == nil {
				continue
			}
			anyMapped = true
			n, ok := pyIntStrict(v)
			if !ok || n < 0 || n >= int64(tracks) {
				valid = false
			}
		}
		if !valid || !anyMapped {
			errors["audio_map"] = fmt.Sprintf("each track must be empty or from 1 to %d for this source", tracks)
		}
	default:
		tracks := src.AudioTracks
		if n, ok := pyIntStrict(audio); !ok || n < 0 || n >= int64(tracks) {
			errors["audio"] = fmt.Sprintf("audio track must be from 1 to %d for this source", tracks)
		}
		if vodTrack {
			if n, ok := pyIntStrict(audioVod); !ok || n < 0 || n >= int64(tracks) {
				errors["audio_vod"] = fmt.Sprintf("VOD audio track must be from 1 to %d for this source", tracks)
			}
		}
	}

	if backupPreset != "" && !containsStr(knownPresetIDs, backupPreset) {
		errors["backup_preset"] = "unknown fallback preset"
	}
	return errors
}

// validateVideoPick — вибір відео платформи: сходинка драбини source-а або
// вся драбина (video<0).
func validateVideoPick(video any, ptype string, src *SourceInfo) map[string]string {
	n, ok := pyIntStrict(video)
	if !ok {
		return map[string]string{"video": "video track must be a number"}
	}
	if src == nil {
		return map[string]string{}
	}
	if n < 0 {
		if ptype != "rtmp" {
			return map[string]string{"video": "the whole ladder can only be sent to an RTMP platform"}
		}
		if !src.EnhancedBroadcasting {
			return map[string]string{"video": "this source carries a single video track -- pick that track"}
		}
		return map[string]string{}
	}
	if !src.EnhancedBroadcasting {
		if n == 0 {
			return map[string]string{}
		}
		return map[string]string{"video": "this source carries a single video track"}
	}
	declared := src.VideoTracks
	if declared == 0 {
		declared = MaxEBVideoTracks
	}
	if int(n) >= declared {
		return map[string]string{"video": fmt.Sprintf("this source declares %d video track(s)", declared)}
	}
	return map[string]string{}
}

// PlatformLimitationWarnings — відомі обмеження площадок (не блокують
// збереження). video не читається тілом функції ні тут, ні в
// мертвий параметр.
func PlatformLimitationWarnings(name, ptype string, vodTrack bool, server string, audioMap any, video any) []string {
	var warnings []string
	host := hostnameOf(server)
	isIVS := host == "live-video.net" || strings.HasSuffix(host, ".live-video.net")

	if ptype == "srt" {
		list, _ := audioMap.([]any)
		mapped := 0
		for _, v := range list {
			if v != nil {
				mapped++
			}
		}
		if mapped > 1 {
			if isIVS {
				warnings = append(warnings, fmt.Sprintf(
					"%s: Kick/IVS SRT ingest accepts exactly ONE audio track -- %d are mapped, "+
						"the ingest will likely keep dropping the connection. Map a single track.", name, mapped))
			} else {
				warnings = append(warnings, fmt.Sprintf(
					"%s: %d audio tracks mapped -- most SRT ingests accept only one. "+
						"If the platform keeps disconnecting, map a single track.", name, mapped))
			}
		}
	} else if vodTrack && host != "" && !(host == "twitch.tv" || strings.HasSuffix(host, ".twitch.tv") || isIVS) {
		warnings = append(warnings, fmt.Sprintf(
			"%s: RTMP+VOD (Twitch VOD track) output is Twitch-specific -- "+
				"this server will likely reject the second audio track.", name))
	}
	return warnings
}

// ValidatePlatformName — валідація створення платформи (ім'я + тип).
func ValidatePlatformName(name, ptype string, existingNames []string) map[string]string {
	errors := validateName(name, existingNames, "platform")
	if !platformTypes[ptype] {
		errors["type"] = "unknown platform type"
	}
	return errors
}

func isRTMP(url string) bool {
	return strings.HasPrefix(url, "rtmp://") || strings.HasPrefix(url, "rtmps://")
}

// hostnameOf — вузький порт urllib.parse.urlsplit(u).hostname (lower-case,
// без userinfo/порту); окрема копія від egress.hostnameOf (там неекспортована).
func hostnameOf(u string) string {
	rest := u
	if i := strings.IndexByte(rest, ':'); i > 0 {
		isScheme := true
		for j := 0; j < i; j++ {
			c := rest[j]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
				isScheme = false
				break
			}
		}
		if isScheme {
			rest = rest[i+1:]
		}
	}
	if !strings.HasPrefix(rest, "//") {
		return ""
	}
	tail := rest[2:]
	netloc := tail
	if delim := strings.IndexAny(tail, "/?#"); delim >= 0 {
		netloc = tail[:delim]
	}
	hostinfo := netloc
	if i := strings.LastIndexByte(netloc, '@'); i >= 0 {
		hostinfo = netloc[i+1:]
	}
	host := hostinfo
	if i := strings.IndexByte(hostinfo, ':'); i >= 0 {
		host = hostinfo[:i]
	}
	return strings.ToLower(host)
}
