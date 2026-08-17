package probe

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"restream_go/internal/proc"
	"restream_go/internal/wire/ts"
)

// Дефолти probe_ts_manifest(url, timeout_sec=8.0, min_window_sec=1.0).
const (
	DefaultManifestTimeoutSec   = 8.0
	DefaultManifestMinWindowSec = 1.0
)

// BuildManifestTransportArgs — argv транспорту для ProbeTSManifest.
func BuildManifestTransportArgs(url string) []string {
	return []string{
		"ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "data", "-i", url,
		"-map", "0", "-c", "copy",
		"-f", "data", "pipe:1",
	}
}

type deadlineReader interface{ SetReadDeadline(time.Time) error }

// ProbeTSManifest — паспорт SRT-readback джерела власним парсером транспорту:
// читає, поки не мине min_window_sec і не буде відома геометрія кожної
// H.264-доріжки, стеля — timeoutSec; ok=false, якщо джерело недоступне/не SRT.
// ffprobe тут не годиться: платить повний analyzeduration і не бачить того,
// що ми реально вміємо демуксувати.
func ProbeTSManifest(url string, timeoutSec, minWindowSec float64) (ts.Manifest, bool) {
	if !strings.HasPrefix(url, "srt://") {
		return ts.Manifest{}, false
	}
	args := BuildManifestTransportArgs(url)
	cmd := exec.Command(args[0], args[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = proc.StartCmd(cmd)
	}
	if err != nil {
		log.Printf("probe: could not start the transport for the source manifest: %v", err)
		return ts.Manifest{}, false
	}

	demuxer, gotData, readErr := readManifest(stdout, timeoutSec, minWindowSec)
	if readErr != nil {
		log.Printf("probe: reading '%s' for the source manifest failed: %v", url, readErr)
	}
	proc.TerminateProc(cmd)
	waitOrKill(cmd, 2*time.Second)
	stdout.Close()

	if !gotData {
		log.Printf("probe: no data read from '%s' -- cannot build the source manifest", url)
		return ts.Manifest{}, false
	}
	demuxer.Flush() // добрати останній PES кожної доріжки (у ньому може бути SPS)
	return demuxer.Manifest(), true
}

// readManifest годує r у свіжий Demuxer, поки не мине minWindowSec і не
// стане повною геометрія кожної H.264-доріжки, або не мине timeoutSec.
// Відокремлено від спавну транспорту заради офлайн-тесту на TS-фікстурах

func readManifest(r io.Reader, timeoutSec, minWindowSec float64) (*ts.Demuxer, bool, error) {
	demuxer := ts.NewDemuxer(func(string, byte, int64, []byte) {}, true, true)
	started := time.Now()
	gotData := false

	dr, hasDeadline := r.(deadlineReader)
	if hasDeadline && dr.SetReadDeadline(time.Time{}) != nil {
		hasDeadline = false
	}

	buf := make([]byte, 65536)
	for {
		elapsed := time.Since(started).Seconds()
		if elapsed >= timeoutSec {
			return demuxer, gotData, nil
		}
		if elapsed >= minWindowSec && demuxer.VideoGeometryComplete() {
			return demuxer, gotData, nil
		}
		if hasDeadline {
			wait := timeoutSec - elapsed
			if wait > 0.25 {
				wait = 0.25
			}
			_ = dr.SetReadDeadline(time.Now().Add(time.Duration(wait * float64(time.Second))))
		}
		n, err := r.Read(buf)
		if n > 0 {
			gotData = true
			demuxer.Feed(buf[:n])
		}
		if err != nil {
			if hasDeadline && errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			if err == io.EOF {
				return demuxer, gotData, nil
			}
			return demuxer, gotData, err
		}
	}
}

// waitOrKill чекає завершення cmd до timeout, інакше Kill (Wait кличеться
// РІВНО раз, у горутині, щоб не подвоїти виклик після форс-завершення).
func waitOrKill(cmd *exec.Cmd, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
}
