package platform

import (
	"math"

	"restream_go/internal/route"
)

// Status — знімок платформи для дашборда
// із замороженими json-ключами.
type Status struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	VODTrack bool   `json:"vod_track"`
	Group    string `json:"group"`
	Source   string `json:"source"`
	Audio    int    `json:"audio"`
	// AudioMap — null для не-srt; незамаплений слот теж null (route.Unmapped).
	AudioMap      []*int       `json:"audio_map"`
	Video         int          `json:"video"`
	EB            bool         `json:"eb"`
	Enabled       bool         `json:"enabled"`
	Gate          bool         `json:"gate"`
	Failed        bool         `json:"failed"`
	State         string       `json:"state"`
	StateSince    float64      `json:"state_since"`
	Halted        bool         `json:"halted"`
	Obs           ObsStatus    `json:"obs"`
	RelayRunning  bool         `json:"relay_running"`
	RelayPID      *int         `json:"relay_pid"`
	BackupRunning bool         `json:"backup_running"`
	BackupPID     *int         `json:"backup_pid"`
	Output        OutputStatus `json:"output"`
}

// ObsStatus — метрики джерела плюс параметри останнього живого потоку. Останні
// п'ять ключів відсутні, поки probe не дав параметрів (гілка
// `if live`).
type ObsStatus struct {
	Flowing    bool    `json:"flowing"`
	VideoKbps  int     `json:"video_kbps"`
	AudioKbps  int     `json:"audio_kbps"`
	Width      *int    `json:"width,omitempty"`
	Height     *int    `json:"height,omitempty"`
	FPS        *int    `json:"fps,omitempty"`
	VideoCodec *string `json:"video_codec,omitempty"`
	AudioCodec *string `json:"audio_codec,omitempty"`
}

// OutputStatus — стан push-виходу платформи.
type OutputStatus struct {
	Running   bool `json:"running"`
	PID       *int `json:"pid"`
	Up        bool `json:"up"`
	UptimeSec int  `json:"uptime_sec"`
	Restarts  int  `json:"restarts"`
	RTTMs     *int `json:"rtt_ms"`
	Dropped   int  `json:"dropped"`
	Behind    bool `json:"behind"`
}

// Status — знімок для дашборда; читається лок-фрі з будь-якої горутини,
// власний стан машини береться однією атомарною парою.
func (m *Machine) Status() Status {
	s := m.snap.Load()
	n := m.nodes.Snapshot()

	st := Status{
		Name:          m.spec.Name,
		Type:          m.spec.Type,
		VODTrack:      m.spec.VODTrack,
		Group:         s.group,
		Source:        m.spec.SourceName,
		Audio:         n.Audio,
		Video:         m.spec.Video(),
		EB:            m.spec.EB,
		Enabled:       s.enabled,
		Gate:          s.gate,
		Failed:        n.Failed,
		State:         s.state.String(),
		StateSince:    s.stateSince,
		Halted:        s.halted,
		Obs:           obsStatus(n),
		RelayRunning:  n.Relay.Running,
		RelayPID:      optInt(n.Relay.PID, n.Relay.HasPID),
		BackupRunning: n.Backup.Running,
		BackupPID:     optInt(n.Backup.PID, n.Backup.HasPID),
		Output: OutputStatus{
			Running:   n.Out.Running,
			PID:       optInt(n.Out.PID, n.Out.HasPID),
			Up:        n.Up,
			UptimeSec: int(math.RoundToEven(n.UptimeSec)),
			Restarts:  n.Restarts,
			RTTMs:     m.rttMs(),
			Dropped:   n.Sink.Dropped,
			Behind:    n.Sink.Behind,
		},
	}
	if m.spec.Type == "srt" {
		st.AudioMap = audioMapStatus(n.AudioMap)
	}
	return st
}

func obsStatus(n NodeStatus) ObsStatus {
	obs := ObsStatus{
		Flowing:   n.Obs.Flowing,
		VideoKbps: n.Obs.VideoKbps,
		AudioKbps: n.Obs.AudioKbps,
	}
	if n.HasLive {
		width, height, fps := n.Live.Width, n.Live.Height, n.Live.FPS
		videoCodec, audioCodec := n.Live.VideoCodec, n.Live.AudioCodec
		obs.Width, obs.Height, obs.FPS = &width, &height, &fps
		obs.VideoCodec, obs.AudioCodec = &videoCodec, &audioCodec
	}
	return obs
}

// audioMapStatus — незамаплений слот назовні йде як null, а не як −1.
func audioMapStatus(m []int) []*int {
	out := make([]*int, len(m))
	for i, track := range m {
		if track == route.Unmapped {
			continue
		}
		v := track
		out[i] = &v
	}
	return out
}

func (m *Machine) rttMs() *int {
	rtt := m.rtt.Load()
	if rtt < 0 {
		return nil
	}
	v := int(rtt)
	return &v
}

func optInt(v int, ok bool) *int {
	if !ok {
		return nil
	}
	value := v
	return &value
}
