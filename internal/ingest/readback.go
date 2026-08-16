// Package ingest будує readback-URL і argv рідерів (ffmpeg RTMP /
// srt-live-transmit) для Manager.readback_url і Platform-рідерів у
package ingest

import "fmt"

// UseSRTReadback — предикат Manager.readback_url: SRT тримає кожну
// доріжку, RTMP сплющує мультитрек до однієї / EB-драбину до legacy AVC.
func UseSRTReadback(audioTracks int, enhancedBroadcasting bool) bool {
	return audioTracks > 1 || enhancedBroadcasting
}

// ReadbackURL — порт Manager.readback_url. path — уже вирішений виклику
// (active_path чи live_path); конкатенація без URL-екранування, як в
// оригіналі.
func ReadbackURL(path string, audioTracks int, enhancedBroadcasting bool, srtPort, rtmpPort int, user, pass string) string {
	if UseSRTReadback(audioTracks, enhancedBroadcasting) {
		return fmt.Sprintf("srt://127.0.0.1:%d?streamid=read:%s:%s:%s", srtPort, path, user, pass)
	}
	return fmt.Sprintf("rtmp://127.0.0.1:%d/%s?user=%s&pass=%s", rtmpPort, path, user, pass)
}
