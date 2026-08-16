package platform

import (
	"restream_go/internal/fallback"
	"restream_go/internal/timeline"
)

// nodes — усе, що стейт-машина робить із вузлами конвеєра. Вузький інтерфейс
// (а не *Pipeline) тримає golden-реплей на фейкових вузлах: слід рішень
// знімається з тих самих викликів.
type nodes interface {
	StartRelay()
	StopRelay()

	StartOutput()
	StopOutput()
	BounceOutput()
	OutputAlive() bool
	Failed() bool
	SetFailed(v bool)

	SetActive(source string)
	RequestSwitch()
	PendingSource() string
	SecondsSinceRelayData() float64

	BackupStart(catchUpSec float64)
	BackupStop()
	BackupRestart()
	HasReadySegment() bool

	ResumeBegin()
	ResumeCancel()
	IsResuming() bool

	PrepareBackup()
	EnsureLadderBackup()
	ApplyPreset() (fallback.LiveParams, bool)
	ResumePreparation(prev fallback.LiveParams, ok bool)

	SetAudio(audio, audioVOD int)
	SetAudioMap(m []int)
	SetCredentials(server, key, streamID, passphrase string) bool

	Shutdown()
	Snapshot() NodeStatus
}

// ProcStatus — процесна частина знімка вузла для дашборда.
type ProcStatus struct {
	Running bool
	PID     int
	HasPID  bool
}

// NodeStatus — усе, що status бере з вузлів конвеєра (решта — власний стан
// машини).
type NodeStatus struct {
	Obs     timeline.SourceStats
	Live    fallback.LiveParams
	HasLive bool

	Audio    int
	AudioMap []int
	Failed   bool

	Relay  ProcStatus
	Backup ProcStatus
	Out    ProcStatus

	Up        bool
	UptimeSec float64
	Restarts  int
	Sink      timeline.SinkStats
}

// pipelineNodes — прод-реалізація: делегує зібраному конвеєру.
type pipelineNodes struct{ p *Pipeline }

func (n pipelineNodes) StartRelay() { n.p.StartRelay() }
func (n pipelineNodes) StopRelay()  { n.p.StopRelay() }

func (n pipelineNodes) StartOutput()      { n.p.StartOutput() }
func (n pipelineNodes) StopOutput()       { n.p.StopOutput() }
func (n pipelineNodes) BounceOutput()     { n.p.BounceOutput() }
func (n pipelineNodes) OutputAlive() bool { return n.p.OutputAlive() }
func (n pipelineNodes) Failed() bool      { return n.p.Failed() }
func (n pipelineNodes) SetFailed(v bool)  { n.p.SetFailed(v) }

func (n pipelineNodes) SetActive(source string) { n.p.Switcher.SetActive(source) }
func (n pipelineNodes) RequestSwitch()          { n.p.requestSwitchToRelay(nil) }
func (n pipelineNodes) PendingSource() string   { return n.p.Switcher.PendingSource() }
func (n pipelineNodes) SecondsSinceRelayData() float64 {
	return n.p.Switcher.SecondsSinceRelayData()
}

func (n pipelineNodes) BackupStart(catchUpSec float64) { n.p.Player.Start(catchUpSec) }
func (n pipelineNodes) BackupStop()                    { n.p.Player.Stop() }
func (n pipelineNodes) BackupRestart()                 { n.p.Player.Restart() }
func (n pipelineNodes) HasReadySegment() bool          { return n.p.Preparer().HasReadySegment() }

func (n pipelineNodes) ResumeBegin()     { n.p.Resume.Begin() }
func (n pipelineNodes) ResumeCancel()    { n.p.Resume.Cancel() }
func (n pipelineNodes) IsResuming() bool { return n.p.Resume.IsResuming() }

func (n pipelineNodes) PrepareBackup()      { n.p.PrepareBackup() }
func (n pipelineNodes) EnsureLadderBackup() { n.p.EnsureLadderBackup() }

func (n pipelineNodes) ApplyPreset() (fallback.LiveParams, bool) { return n.p.ApplyPreset() }

func (n pipelineNodes) ResumePreparation(prev fallback.LiveParams, ok bool) {
	n.p.ResumePreparation(prev, ok)
}

func (n pipelineNodes) SetAudio(audio, audioVOD int) { n.p.SetAudio(audio, audioVOD) }
func (n pipelineNodes) SetAudioMap(m []int)          { n.p.SetAudioMap(m) }

func (n pipelineNodes) SetCredentials(server, key, streamID, passphrase string) bool {
	return n.p.SetCredentials(server, key, streamID, passphrase)
}

func (n pipelineNodes) Shutdown() { n.p.Shutdown() }

func (n pipelineNodes) Snapshot() NodeStatus {
	p := n.p
	live, hasLive := p.Preparer().LastLiveParams()
	relayPID, relayHasPID := p.Relay.PID()
	backupPID, backupHasPID := p.Player.PID()
	outPID, outHasPID := p.Out.PID()
	return NodeStatus{
		Obs:       p.Switcher.SourceStats(),
		Live:      live,
		HasLive:   hasLive,
		Audio:     p.Audio(),
		AudioMap:  p.AudioMap(),
		Failed:    p.Failed(),
		Relay:     ProcStatus{Running: p.Relay.IsRunning(), PID: relayPID, HasPID: relayHasPID},
		Backup:    ProcStatus{Running: p.Player.IsRunning(), PID: backupPID, HasPID: backupHasPID},
		Out:       ProcStatus{Running: p.Out.IsRunning(), PID: outPID, HasPID: outHasPID},
		Up:        p.Out.EverRanLong(),
		UptimeSec: p.Out.UptimeSec(),
		Restarts:  p.Out.RestartCount(),
		Sink:      p.Sink.Stats(),
	}
}
