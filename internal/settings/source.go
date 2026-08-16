package settings

import "fmt"

var sourceTypes = map[string]bool{"rtmp": true, "srt": true}

// MaxSRTAudioTracks — стеля декларованих SRT-доріжок.
const MaxSRTAudioTracks = 6

// MaxEBVideoTracks — стеля сходинок EB-драбини на канвас.
const MaxEBVideoTracks = 5

// ValidateSource — валідація одного source (add/update).
func ValidateSource(name, stype string, audioTracks any, existingNames []string,
	enhancedBroadcasting bool, videoTracks any) map[string]string {
	errors := validateName(name, existingNames, "source")

	switch {
	case !sourceTypes[stype]:
		errors["type"] = "unknown source type"
	case stype == "srt":
		if n, ok := pyIntStrict(audioTracks); !ok || n < 1 || n > MaxSRTAudioTracks {
			errors["audio_tracks"] = fmt.Sprintf("must be a number from 1 to %d", MaxSRTAudioTracks)
		}
		if enhancedBroadcasting {
			errors["enhanced_broadcasting"] = "Enhanced Broadcasting is an RTMP-only feature"
		}
	case enhancedBroadcasting:
		if n, ok := pyIntStrict(videoTracks); !ok || n < 0 || n > MaxEBVideoTracks {
			errors["video_tracks"] = fmt.Sprintf("must be AUTO or a number from 1 to %d", MaxEBVideoTracks)
		}
	}
	return errors
}

// ValidateSourceName — валідація створення source (ім'я + тип).
func ValidateSourceName(name, stype string, existingNames []string) map[string]string {
	errors := validateName(name, existingNames, "source")
	if !sourceTypes[stype] {
		errors["type"] = "unknown source type"
	}
	return errors
}
