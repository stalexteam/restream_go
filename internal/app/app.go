// Package app — тіло демона restreamd.
package app

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"restream_go/internal/api"
	"restream_go/internal/control"
	"restream_go/internal/media"
	"restream_go/internal/mtx"
	"restream_go/internal/settings"
)

// mtxKeys — рівно ті ключі конфіга, які читає mtx.Render.
var mtxKeys = []string{"obs_pass", "internal_pass", "connect_timeout_ms", "read_timeout_ms",
	"mediamtx_rtmp_port", "mediamtx_srt_port", "listen_port"}

func Main() {
	baseDir := baseDirFromExecutable()
	configPath := filepath.Join(baseDir, "config.json")
	// Сетап логує ще до відкриття лог-файлу.
	log.SetFlags(0)

	switch {
	case len(os.Args) == 1:
	case len(os.Args) == 2 && isConfigFlag(os.Args[1]):
		os.Exit(runSetup(baseDir, configPath, os.Stdin, os.Stdout))
	default:
		printUsage(os.Stderr)
		os.Exit(2)
	}

	if err := ensureDirs(baseDir); err != nil {
		fmt.Fprintf(os.Stderr, "could not create the working directories in %s: %v\n", baseDir, err)
		os.Exit(1)
	}
	config, err := validateStartup(baseDir, configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logDir := filepath.Join(baseDir, "logs")
	logPath := filepath.Join(logDir, "controller.log")
	if custom, ok := config.Get("log_file"); ok {
		if text, ok := custom.(string); ok && text != "" {
			logPath = text
		}
	}
	if err := setupLogging(logPath); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open log file %s: %v\n", logPath, err)
		os.Exit(1)
	}

	// listen_* читаються ДО Manager: після New конфіг мутують чужі горутини.
	hostText := pyStr(config.GetOr("listen_host", "0.0.0.0"))
	portText := pyStr(config.GetOr("listen_port", defaultPort))
	token := ""
	if value := config.GetOr("dashboard_token", nil); pyTruthy(value) {
		token = pyStr(value)
	}

	manager := control.New(config, control.Options{
		BaseDir: baseDir, ConfigPath: configPath, Ping: mtx.Probe,
	})
	// Hub будує знімки з manager-а, а manager сповіщає hub про зміни --
	// взаємна залежність, тому колбеки чіпляються постфактум.
	hub := api.NewHub(api.ManagerSource{M: manager}, baseDir)
	manager.OnChange = hub.Notify
	manager.OnEvent = hub.PushEvent
	manager.OnControl = hub.PushControl

	store := media.NewStore(settings.BackupRoot(baseDir))
	store.OnDurationsReady = func(pathRel string) {
		var data any
		if listing, ok := store.ListDir(pathRel); ok {
			data = listing
		}
		hub.PushMessage(filesMessage{Type: "files", Data: data})
	}

	mtxCtl := mtx.NewController(baseDir)
	server := api.NewServer(api.Deps{
		Manager: manager, Media: store, Hub: hub,
		BaseDir: baseDir, ConfigPath: configPath, Token: token,
		RestartMediaMTX: func() error { return mtxCtl.Restart(mtxConfig(manager)) },
	})

	go mtx.Watch(filepath.Join(logDir, "mediamtx.log"), manager.OnMediaMTXConnectTimeout, nil)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signals
		log.Printf("received termination signal (%s), stopping ffmpeg processes", signalText(sig))
		manager.Shutdown()
		_ = mtxCtl.Stop()
		os.Exit(0)
	}()

	// Слухач піднімається ДО MediaMTX: інакше його перший хук може влетіти
	// у connection refused.
	addr := net.JoinHostPort(hostText, portText)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("could not listen on %s: %v", addr, err)
		os.Exit(1)
	}
	if err := mtxCtl.Restart(mtxConfig(manager)); err != nil {
		log.Printf("could not start mediamtx: %v", err)
	}

	log.Printf("controller started on %s:%s", hostText, portText)
	httpServer := server.HTTPServer(addr)
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http server stopped: %v", err)
	}
	manager.Shutdown()
}

// isConfigFlag — єдиний аргумент, який розуміє бінар (обидві форми).
func isConfigFlag(arg string) bool { return arg == "--config" || arg == "/config" }

func printUsage(w io.Writer) {
	name := filepath.Base(os.Args[0])
	fmt.Fprintf(w, "usage: %s [--config]\n", name)
	fmt.Fprintf(w, "  (no arguments)     start the controller with config.json next to the binary\n")
	fmt.Fprintf(w, "  --config, /config  interactive setup: create or repair config.json and the\n")
	fmt.Fprintf(w, "                     OBS files, then exit without starting the controller\n")
}

// filesMessage — кадр `{"type": "files", "data":...}` фонових тривалостей.
type filesMessage struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// baseDirFromExecutable — аналог Path(__file__).parent.parent: каталог бінаря,
// а для штатного макета <base>/bin/restreamd — його батько.
func baseDirFromExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if strings.EqualFold(filepath.Base(dir), "bin") {
		return filepath.Dir(dir)
	}
	return dir
}

// mtxConfig знімає ключі mtx.Render із ЖИВОГО конфіга (Settings Apply мутує
// його на місці) — читанням під локом менеджера.
func mtxConfig(m *control.Manager) map[string]any {
	out := make(map[string]any, len(mtxKeys))
	for _, key := range mtxKeys {
		if value, ok := m.ConfigValue(key); ok {
			out[key] = value
		}
	}
	return out
}

func setupLogging(path string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	log.SetFlags(0)
	log.SetOutput(&pyLogWriter{w: io.MultiWriter(file, os.Stdout)})
	return nil
}

// pyLogWriter друкує рядки у форматі "%(asctime)s [%(levelname)s]
// %(message)s"; рівень завжди INFO — інших у клоні немає.
type pyLogWriter struct{ w io.Writer }

func (p *pyLogWriter) Write(line []byte) (int, error) {
	now := time.Now()
	prefix := fmt.Sprintf("%s,%03d [INFO] ", now.Format("2006-01-02 15:04:05"), now.Nanosecond()/1e6)
	if _, err := p.w.Write(append([]byte(prefix), line...)); err != nil {
		return 0, err
	}
	return len(line), nil
}

// signalText — у лозі йде ЧИСЛО signum.
func signalText(sig os.Signal) string {
	if s, ok := sig.(syscall.Signal); ok {
		return strconv.Itoa(int(s))
	}
	return fmt.Sprint(sig)
}
