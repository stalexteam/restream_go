package app

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"restream_go/internal/assets"
	"restream_go/internal/control"
)

// secretKeys — поля config.json, які сетап генерує сам (hex).
var secretKeys = []string{"obs_pass", "internal_pass", "dashboard_token"}

// hostRe — та сама перевірка hostname/IP, що робив install.sh.
var hostRe = regexp.MustCompile(`^[A-Za-z0-9_:][A-Za-z0-9._:-]*$`)

// runSetup — режим --config: готує config.json, питає public_host і рендерить
// OBS-файли; демон не стартує.
func runSetup(baseDir, configPath string, in io.Reader, out io.Writer) int {
	if err := ensureDirs(baseDir); err != nil {
		fmt.Fprintf(os.Stderr, "could not create the working directories in %s: %v\n", baseDir, err)
		return 1
	}
	config, err := ensureConfig(baseDir, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not prepare %s: %v\n", configPath, err)
		return 1
	}

	current := strings.TrimSpace(pyStr(config.GetOr("public_host", "")))
	if current == "None" || isPlaceholder(current) {
		current = placeholderHost
	}
	config.Set("public_host", promptPublicHost(in, out, current))
	if err := control.Persist(configPath, config); err != nil {
		fmt.Fprintf(os.Stderr, "could not save %s: %v\n", configPath, err)
		return 1
	}
	_ = os.Chmod(configPath, 0o600)

	if err := writeOBSFiles(baseDir, config); err != nil {
		fmt.Fprintf(os.Stderr, "could not write the OBS files: %v\n", err)
		return 1
	}
	printSetupSummary(out, baseDir, config)
	return 0
}

// promptPublicHost — порожній ввід, EOF або несхоже на host значення лишають
// поточне.
func promptPublicHost(in io.Reader, out io.Writer, current string) string {
	fmt.Fprintf(out, "Public IP or hostname of this server [%s]: ", current)
	line, err := bufio.NewReader(in).ReadString('\n')
	answer := strings.TrimSpace(line)
	if err != nil && answer == "" {
		fmt.Fprintln(out)
		return current
	}
	if answer == "" {
		return current
	}
	if !hostRe.MatchString(answer) {
		fmt.Fprintf(out, "'%s' doesn't look like a hostname or IP -- keeping %s\n", answer, current)
		return current
	}
	return answer
}

const (
	ansiBold  = "\033[1m"
	ansiCyan  = "\033[36m"
	ansiRed   = "\033[31m"
	ansiReset = "\033[0m"
)

// setupHints — платформозалежні рядки фінальної інструкції.
type setupHints struct {
	start    string
	stop     string
	logs     string
	notes    []string // примітки під командами
	firewall string   // команда фаєрвола; порожня -- рядок не друкується
}

// hintsFor — команди керування й фаєрвола для цільової ОС: Linux має
// systemd-юніт від install.sh, решта запускає бінар руками.
func hintsFor(goos, baseDir, port, rtmpPort string) setupHints {
	logPath := filepath.Join(baseDir, "logs", "controller.log")
	switch goos {
	case "windows":
		return setupHints{
			start: filepath.Join(baseDir, "restreamd.exe"),
			stop:  "Ctrl+C in that window",
			logs:  logPath,
			firewall: fmt.Sprintf(
				`netsh advfirewall firewall add rule name="restreamd" dir=in action=allow `+
					`protocol=TCP localport=%s,%s remoteip=<your-ip>`, rtmpPort, port),
		}
	case "linux":
		return setupHints{
			start: "sudo systemctl start restreamd",
			stop:  "sudo systemctl stop restreamd",
			logs:  "journalctl -u restreamd -f",
			notes: []string{
				"log file (ffmpeg included): " + logPath,
				"no systemd unit? run ./restreamd from " + baseDir,
			},
			firewall: fmt.Sprintf("ufw allow from <your-ip> to any port %s,%s proto tcp",
				rtmpPort, port),
		}
	default:
		return setupHints{
			start: "./restreamd    (from " + baseDir + ")",
			stop:  "Ctrl+C in that window",
			logs:  logPath,
		}
	}
}

// printSetupSummary — фінальна інструкція: перегенеровані OBS-файли, дашборд,
// керування демоном і фаєрвол; решта -- у Doc/Setup.
func printSetupSummary(out io.Writer, baseDir string, config *control.Dict) {
	host := pyStr(config.GetOr("public_host", placeholderHost))
	port := pyStr(config.GetOr("listen_port", defaultPort))
	rtmpPort := pyStr(config.GetOr("mediamtx_rtmp_port", "1935"))
	srtPort := pyStr(config.GetOr("mediamtx_srt_port", "8890"))
	hints := hintsFor(runtime.GOOS, baseDir, port, rtmpPort)
	value := func(text string) string { return ansiBold + ansiCyan + text + ansiReset }
	line := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }

	line("")
	line("======================================================================")
	line("Configuration complete: %s", filepath.Join(baseDir, "config.json"))
	line("")
	line("CONTROL:")
	line("  start  %s", value(hints.start))
	line("  stop   %s", value(hints.stop))
	line("  logs   %s", value(hints.logs))
	for _, note := range hints.notes {
		line("  %s", note)
	}
	line("")
	line("OBS FILES:")
	line("  Custom Browser Dock (OBS -> Docks):")
	line("    %s", value(filepath.Join(baseDir, "obs-dock.html")))
	line("  Browser Source (in a scene, 320x32):")
	line("    %s", value(filepath.Join(baseDir, "obs-source.html")))
	line("")

	line("DASBOARD:")
	line("  URL:  %s", value(fmt.Sprintf("http://%s:%s/dashboard?token=%s",
		host, port, pyStr(config.GetOr("dashboard_token", "")))))
	line("")

	printFirewallNote(out, port, rtmpPort, srtPort, hints.firewall)
	line("======================================================================")
}

func printFirewallNote(out io.Writer, port, rtmpPort, srtPort, command string) {
	red := func(text string) string { return ansiRed + text + ansiReset }
	redBold := func(text string) string { return ansiRed + ansiBold + text + ansiReset + ansiRed }
	line := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }

	line("%s%sFIREWALL:%s", ansiBold, ansiRed, ansiReset)
	line(red("  Ports you need to OPEN (ideally only to your own IP(s) -- the"))
	line(red("  token/RTMP password travel unencrypted over plain HTTP/RTMP):"))
	line(red(fmt.Sprintf("    %s  RTMP ingest from OBS", redBold(rtmpPort+"/tcp"))))
	line(red(fmt.Sprintf("    %s  dashboard + WebSocket (OBS dock/source, browser)", redBold(port+"/tcp"))))
	line(red(fmt.Sprintf("    %s  SRT ingest (ONLY if you use SRT sources)", redBold(srtPort+"/udp"))))
	line(red("  Keep CLOSED (disabled or localhost-only):"))
	line(red(fmt.Sprintf("    %s RTSP (localhost-only)   %s HLS   %s WebRTC   %s MoQ   %s control API",
		redBold("8554"), redBold("8888"), redBold("8889"), redBold("8892"), redBold("9997"))))
	if command != "" {
		line(red(fmt.Sprintf("  %s", redBold(command))))
	}
	line("")
}

// ensureConfig повертає готовий до роботи конфіг: створює його з вшитого
// прикладу, якщо файлу немає чи він битий, і доливає лише відсутні ключі.
func ensureConfig(baseDir, configPath string) (*control.Dict, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		log.Printf("setup: %s not found -- creating it from the built-in example", configPath)
		return writeFreshConfig(baseDir, configPath)
	}

	config, err := control.Loads(raw)
	if err != nil {
		broken := fmt.Sprintf("%s.broken-%d", configPath, time.Now().Unix())
		log.Printf("setup: %s is not valid JSON (%v) -- keeping it as %s and creating a fresh one",
			configPath, err, broken)
		if err := os.Rename(configPath, broken); err != nil {
			return nil, err
		}
		return writeFreshConfig(baseDir, configPath)
	}

	added, err := fillMissingKeys(baseDir, configPath, config)
	if err != nil {
		return nil, err
	}
	if len(added) > 0 {
		log.Printf("setup: added missing config keys from the built-in example: %s", strings.Join(added, ", "))
	}
	return config, nil
}

// writeFreshConfig пише приклад із підставленими секретами й baseDir.
func writeFreshConfig(baseDir, configPath string) (*control.Dict, error) {
	text, err := renderExample(baseDir)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		return nil, err
	}
	return control.Loads([]byte(text))
}

// renderExample — вшитий config.example.json із заповненими плейсхолдерами;
// public_host лишається заглушкою YOUR_VPS_IP до промпта.
func renderExample(baseDir string) (string, error) {
	text := assets.ConfigExample
	for _, key := range secretKeys {
		secret, err := genSecret()
		if err != nil {
			return "", err
		}
		text = strings.ReplaceAll(text, "__"+strings.ToUpper(key)+"__", secret)
	}
	text = strings.ReplaceAll(text, "__PUBLIC_HOST__", placeholderHost)
	text = strings.ReplaceAll(text, "__BASE_DIR__", baseDir)
	return text, nil
}

// fillMissingKeys доливає в config відсутні ключі прикладу (секрети —
// свіжозгенеровані) і персистить, якщо щось змінилось.
func fillMissingKeys(baseDir, configPath string, config *control.Dict) ([]string, error) {
	text, err := renderExample(baseDir)
	if err != nil {
		return nil, err
	}
	example, err := control.Loads([]byte(text))
	if err != nil {
		return nil, err
	}

	var added []string
	for _, key := range example.Keys() {
		if !config.Has(key) {
			config.Set(key, example.GetOr(key, nil))
			added = append(added, key)
		}
	}
	for _, key := range secretKeys {
		if value := pyStr(config.GetOr(key, "")); value == "" || isPlaceholder(value) {
			config.Set(key, example.GetOr(key, ""))
			if !containsText(added, key) {
				added = append(added, key)
			}
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := control.Persist(configPath, config); err != nil {
		return nil, err
	}
	// Persist кладе 0644, а тут у файлі паролі й токен.
	_ = os.Chmod(configPath, 0o600)
	return added, nil
}

// writeOBSFiles рендерить obs-dock.html і obs-source.html у baseDir; кличе це
// лише режим --config, уже після збереження фінального конфіга.
func writeOBSFiles(baseDir string, config *control.Dict) error {
	host := pyStr(config.GetOr("public_host", ""))
	if host == "" || isPlaceholder(host) {
		host = placeholderHost
	}
	port := pyStr(config.GetOr("listen_port", ""))
	if port == "" {
		port = defaultPort
	}
	token := pyStr(config.GetOr("dashboard_token", ""))

	dock := strings.ReplaceAll(assets.OBSDockTemplate, "__DASHBOARD_URL__",
		fmt.Sprintf("http://%s:%s/dashboard?token=%s", host, port, token))
	source := strings.ReplaceAll(assets.OBSSourceTemplate, "__WS_URL__",
		fmt.Sprintf("ws://%s:%s/ws?token=%s", host, port, token))

	for name, text := range map[string]string{"obs-dock.html": dock, "obs-source.html": source} {
		path := filepath.Join(baseDir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			return err
		}
		// Windows не має unix-прав: помилка chmod тут не показова.
		_ = os.Chmod(path, 0o600)
	}
	if host == placeholderHost {
		log.Printf("setup: public_host is still %s -- the OBS files will not reach this server "+
			"until you run './restreamd --config'", placeholderHost)
	}
	return nil
}

const (
	placeholderHost = "YOUR_VPS_IP"
	defaultPort     = "8790"
)

// ensureDirs створює робочі каталоги; tmp/ і logs/ несуть секрети в
// mediamtx.yml та ffmpeg-командах, тому 0700.
func ensureDirs(baseDir string) error {
	for _, dir := range []struct {
		name string
		mode os.FileMode
	}{{"logs", 0o700}, {"tmp", 0o700}, {"media", 0o755}} {
		if err := os.MkdirAll(filepath.Join(baseDir, dir.name), dir.mode); err != nil {
			return err
		}
	}
	return nil
}

func genSecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// isPlaceholder — незамінений маркер виду __NAME__.
func isPlaceholder(value string) bool {
	return strings.HasPrefix(value, "__") && strings.HasSuffix(value, "__")
}

func containsText(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
