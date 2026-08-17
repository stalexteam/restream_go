package mtx

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"restream_go/internal/proc"
)

const (
	defaultStopTimeout       = 5 * time.Second
	defaultStartupCheckDelay = 1 * time.Second
)

// Controller володіє ЄДИНИМ живим MediaMTX-процесом контролера (inventory
// рішення: дитина контролера, живе й вмирає разом, P1-захист). Керує ним
// через child-хендл у памʼяті, а не через pid-файл; сам pid-файл
// пишеться далі (його читає дашборд), але джерелом правди не є.
type Controller struct {
	binPath  string
	yamlPath string
	logPath  string
	dataDir  string
	pidPath  string

	// stopTimeout/startupCheckDelay — шов для тестів; прод
	// лишає дефолти шаблона.
	stopTimeout       time.Duration
	startupCheckDelay time.Duration

	mu   sync.Mutex
	proc *runningProc
}

type runningProc struct {
	cmd     *exec.Cmd
	logFile *os.File
	exited  chan struct{}
}

// ConfigPath — mediamtx.yml у tmp/ інсталяції; на WSL-шарі MediaMTX гине на
// стеженні за конфігом, тому звідти конфіг їде в %TEMP%.
func ConfigPath(baseDir string) string {
	if !onWSLShare(baseDir) {
		return filepath.Join(baseDir, "tmp", "mediamtx.yml")
	}
	sum := sha256.Sum256([]byte(strings.ToLower(baseDir)))
	return filepath.Join(os.TempDir(), "restreamd-"+hex.EncodeToString(sum[:4]), "mediamtx.yml")
}

// onWSLShare — 9P-шара WSL, де немає ReadDirectoryChangesW.
func onWSLShare(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasPrefix(lower, `\\wsl.localhost\`) || strings.HasPrefix(lower, `\\wsl$\`)
}

// BinaryPath — бінар MediaMTX у штатному макеті.
func BinaryPath(baseDir string) string {
	name := "mediamtx"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(baseDir, "bin", name)
}

// NewController — штатний макет каталогів (bin/mediamtx, tmp/mediamtx.yml,
// logs/mediamtx.log).
func NewController(baseDir string) *Controller {
	return &Controller{
		binPath:           BinaryPath(baseDir),
		yamlPath:          ConfigPath(baseDir),
		logPath:           filepath.Join(baseDir, "logs", "mediamtx.log"),
		dataDir:           filepath.Join(baseDir, "tmp"),
		pidPath:           filepath.Join(baseDir, "tmp", ".mediamtx.pid"),
		stopTimeout:       defaultStopTimeout,
		startupCheckDelay: defaultStartupCheckDelay,
	}
}

// Restart рендерить mediamtx.yml, зупиняє поточний інстанс (якщо є) і
// піднімає новий; ним же стартує перший раз.
func (c *Controller) Restart(config map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := Render(TemplateText, c.yamlPath, config); err != nil {
		return err
	}
	c.stopLocked()
	return c.startLocked()
}

// Stop зупиняє MediaMTX, якщо запущений (graceful shutdown контролера; P1
// однаково гарантує це при вбивстві контролера).
func (c *Controller) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
	return nil
}

// stopLocked — SIGTERM, чекання до stopTimeout, SIGKILL при невдачі (порт
// _stop_existing,:47, адаптований під in-memory хендл замість pid-файлу).
func (c *Controller) stopLocked() {
	p := c.proc
	if p == nil {
		return
	}
	c.proc = nil

	log.Printf("stopping mediamtx (pid=%d) for restart", p.cmd.Process.Pid)
	proc.TerminateProc(p.cmd)
	if !exitedWithin(p.exited, c.stopTimeout) {
		log.Printf("mediamtx (pid=%d) did not exit within %.1fs -- killing it",
			p.cmd.Process.Pid, c.stopTimeout.Seconds())
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
	_ = os.Remove(c.pidPath)
	_ = p.logFile.Close()
}

// startLocked — порт _start_new,:71: stdout/stderr у log_path (append),
// stdin DEVNULL, cwd=tmp/ для відносних артефактів MediaMTX.
func (c *Controller) startLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(c.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	cmd := exec.Command(c.binPath, c.yamlPath)
	cmd.Dir = c.dataDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := proc.StartCmd(cmd); err != nil {
		_ = logFile.Close()
		return err
	}

	// pid-файл — лише для індикатора дашборда (він же перевіряє живість pid);
	// сталий файл після крашу контролера перезапише наступний старт.
	if err := os.WriteFile(c.pidPath, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		log.Printf("could not write %s: %v", c.pidPath, err)
	}

	p := &runningProc{cmd: cmd, logFile: logFile, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	c.proc = p
	log.Printf("mediamtx restarted (pid=%d)", cmd.Process.Pid)

	if exitedWithin(p.exited, c.startupCheckDelay) {
		log.Printf("mediamtx failed to start after restart (pid=%d) -- check %s", cmd.Process.Pid, c.logPath)
	}
	return nil
}

// exitedWithin — true, якщо ch закрився (процес завершився) ДО спливання d.
func exitedWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}
