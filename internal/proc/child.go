package proc

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Started — що супервізор віддає в OnStart (аналог subprocess.Popen у хука).
type Started struct {
	PID    int
	Stdin  io.WriteCloser
	Stdout io.Reader
}

type spawnSpec struct {
	args          []string
	logFile       *os.File
	captureStdout bool
	stdinPipe     bool
}

// child — процес очима супервізора; шов для golden-реплею.
type child interface {
	pid() int
	alive() bool
	wait()
	waitFor(time.Duration) bool
	terminate()
	kill()
	hasStdin() bool
	closeStdin()
	started() *Started
	closePipes()
}

type execChild struct {
	cmd     *exec.Cmd
	in      *os.File
	outRead *os.File
	out     io.Reader
	done    chan struct{}
	exited  atomic.Bool
	inOnce  sync.Once
	outOnce sync.Once
}

// spawnExec — прод-шлях: stdout/stderr (capture_stdout →
// stdout у пайп і stderr у лог, інакше обидва в лог), stdin — пайп або devnull.
func spawnExec(spec spawnSpec) (child, error) {
	cmd := exec.Command(spec.args[0], spec.args[1:]...)
	c := &execChild{cmd: cmd, done: make(chan struct{})}

	var childOut, childIn *os.File
	fail := func(err error) (child, error) {
		closeFile(childOut)
		closeFile(childIn)
		c.closePipes()
		return nil, err
	}

	if spec.captureStdout {
		r, w, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		c.outRead, c.out, childOut = r, wrapPipe(r), w
		cmd.Stdout = w
		cmd.Stderr = spec.logFile
	} else {
		cmd.Stdout = spec.logFile
		cmd.Stderr = spec.logFile
	}
	if spec.stdinPipe {
		r, w, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		c.in, childIn = w, r
		cmd.Stdin = r
	}

	if err := StartCmd(cmd); err != nil {
		return fail(err)
	}
	closeFile(childOut)
	closeFile(childIn)

	go func() {
		_ = cmd.Wait()
		c.exited.Store(true)
		close(c.done)
	}()
	return c, nil
}

func closeFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func (c *execChild) pid() int {
	if c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

func (c *execChild) alive() bool { return !c.exited.Load() }

func (c *execChild) wait() { <-c.done }

func (c *execChild) waitFor(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.done:
		return true
	case <-timer.C:
		return false
	}
}

func (c *execChild) terminate() { TerminateProc(c.cmd) }

func (c *execChild) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func (c *execChild) hasStdin() bool { return c.in != nil }

func (c *execChild) closeStdin() {
	if c.in == nil {
		return
	}
	c.inOnce.Do(func() { _ = c.in.Close() })
}

func (c *execChild) started() *Started {
	s := &Started{PID: c.pid(), Stdout: c.out}
	if c.in != nil {
		s.Stdin = c.in
	}
	return s
}

// closePipes звільняє батьківські кінці пайпів.
func (c *execChild) closePipes() {
	c.closeStdin()
	c.outOnce.Do(func() {
		if d, ok := c.out.(*DeadlineReader); ok {
			_ = d.Close()
		}
		closeFile(c.outRead)
	})
}
