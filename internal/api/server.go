package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"restream_go/internal/control"
	"restream_go/internal/media"
	"restream_go/internal/web"
)

// Ліміти запиту.
const (
	maxRequestBody = 65536
	uploadChunk    = 256 * 1024
	mediaChunk     = 256 * 1024
	drainLimit     = 32 * 1024 * 1024
	// SocketTimeout — вікно простою на КОЖНОМУ читанні запиту.
	SocketTimeout = 15 * time.Second
)

var staticContentTypes = map[string]string{
	"common.css":    "text/css; charset=utf-8",
	"dashboard.css": "text/css; charset=utf-8",
	"config.css":    "text/css; charset=utf-8",
	"shared.js":     "application/javascript; charset=utf-8",
	"dashboard.js":  "application/javascript; charset=utf-8",
	"config.js":     "application/javascript; charset=utf-8",
}

var pages = map[string]string{
	"/dashboard": "index.html",
	"/config":    "config.html",
}

var rangeRe = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// Deps — залежності HTTP/WS-шару (аргументи make_handler плюс шви cmd).
type Deps struct {
	Manager    *control.Manager
	Media      *media.Store
	Hub        *Hub
	BaseDir    string
	ConfigPath string
	// Dashboard — джерело статики дашборда; порожнє → вшита web.Dashboard.
	Dashboard fs.FS
	Token     string

	// RestartMediaMTX — рестарт MediaMTX після зміни connect/read таймінгів.
	RestartMediaMTX func() error
}

// Server — HTTP-обробник контролера.
type Server struct {
	d Deps
}

// NewServer збирає обробник.
func NewServer(d Deps) *Server {
	if d.Dashboard == nil {
		d.Dashboard = web.Dashboard
	}
	return &Server{d: d}
}

// HTTPServer — сервер із 15-секундними таймаутами.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: SocketTimeout,
		IdleTimeout:       SocketTimeout,
	}
}

// DashboardTokenOK — порожній або незамінений плейсхолдер відхиляється,
// решта звіряється constant-time.
func DashboardTokenOK(provided, expected string) bool {
	if expected == "" || strings.HasPrefix(expected, "__") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.doGET(w, r)
	case http.MethodPost:
		s.doPOST(w, r)
	default:
		http.Error(w, fmt.Sprintf("Unsupported method (%q)", r.Method), http.StatusNotImplemented)
	}
}

func (s *Server) doGET(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	query := r.URL.Query()

	switch {
	case path == "/status":
		if !isLocalhost(r) {
			s.send(w, http.StatusForbidden, errorBody("forbidden"))
			return
		}
		s.send(w, http.StatusOK, s.d.Manager.Status())
	case pages[path] != "":
		if !s.checkToken(query) {
			s.send(w, http.StatusUnauthorized, errorBody("invalid token"))
			return
		}
		// Referrer-Policy: токен їде в query самої сторінки.
		s.sendFile(w, pages[path], "text/html; charset=utf-8",
			[2]string{"Referrer-Policy", "no-referrer"})
	case staticContentTypes[strings.TrimLeft(path, "/")] != "":
		name := strings.TrimLeft(path, "/")
		s.sendFile(w, name, staticContentTypes[name])
	case path == "/ws":
		if !s.checkToken(query) {
			s.send(w, http.StatusUnauthorized, errorBody("invalid token"))
			return
		}
		s.serveWS(w, r)
	case path == "/files/raw":
		if !s.checkToken(query) {
			s.send(w, http.StatusUnauthorized, errorBody("invalid token"))
			return
		}
		s.serveMedia(w, r, query.Get("path"))
	default:
		s.send(w, http.StatusNotFound, errorBody("not found"))
	}
}

func (s *Server) doPOST(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	// Аплоад читає тіло сам — маршрут розбирається ДО загального ліміту.
	if r.URL.Path == "/files/upload" {
		s.handleUpload(w, r, query)
		return
	}

	length, ok := pyContentLength(r)
	if !ok {
		s.send(w, http.StatusBadRequest, errorBody("bad Content-Length"))
		return
	}
	if length < 0 || length > maxRequestBody {
		s.send(w, http.StatusRequestEntityTooLarge, errorBody("request body too large"))
		return
	}
	if length > 0 {
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(SocketTimeout))
		_, _ = io.CopyN(io.Discard, r.Body, length)
		_ = rc.SetReadDeadline(time.Time{})
	}

	mtxPath := query.Get("path")
	switch r.URL.Path {
	case "/hooks/available":
		if !isLocalhost(r) {
			s.send(w, http.StatusForbidden, errorBody("forbidden"))
			return
		}
		s.d.Manager.OnAvailable(mtxPath)
		s.send(w, http.StatusOK, okBody())
	case "/hooks/unavailable":
		if !isLocalhost(r) {
			s.send(w, http.StatusForbidden, errorBody("forbidden"))
			return
		}
		s.d.Manager.OnUnavailable(mtxPath)
		s.send(w, http.StatusOK, okBody())
	default:
		s.send(w, http.StatusNotFound, errorBody("not found"))
	}
}

// --- аплоад ---

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, query url.Values) {
	w.Header().Set("Connection", "close")
	if !s.checkToken(query) {
		s.send(w, http.StatusUnauthorized, errorBody("invalid token"))
		return
	}
	size, ok := pyContentLength(r)
	if !ok {
		s.send(w, http.StatusBadRequest, errorBody("bad Content-Length"))
		return
	}
	if size <= 0 {
		s.send(w, http.StatusBadRequest, errorBody("empty upload"))
		return
	}

	store := s.d.Media
	// Один аплоад за раз: два паралельні змагались би за диск і бюджет заглушки.
	if !store.AcquireUploadSlot() {
		s.send(w, http.StatusConflict, errorBody("another upload is already running"))
		return
	}
	defer store.ReleaseUploadSlot()

	part, final, errMsg := store.PrepareUpload(query.Get("path"), query.Get("name"), size)
	if errMsg != "" {
		s.rejectBeforeBody(w, r, size, errMsg)
		return
	}
	if reason := s.receiveBody(w, r, part, size); reason != "" {
		_ = os.Remove(part)
		s.send(w, http.StatusBadRequest, errorBody(reason))
		return
	}
	rel, errMsg, storeFailed := store.FinalizeUpload(part, final)
	if errMsg != "" {
		code := http.StatusBadRequest
		if storeFailed {
			code = http.StatusInternalServerError
		}
		s.send(w, code, errorBody(errMsg))
		return
	}
	log.Printf("upload: stored backup/%s (%d bytes)", rel, size)
	s.send(w, http.StatusOK, uploadedBody(rel))
}

// rejectBeforeBody — дочитати тіло до стелі й лише тоді відмовити: інакше
// клієнт отримає RST замість причини.
func (s *Server) rejectBeforeBody(w http.ResponseWriter, r *http.Request, size int64, errMsg string) {
	remaining := size
	if remaining < 0 {
		remaining = 0
	}
	if remaining > drainLimit {
		remaining = drainLimit
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, uploadChunk)
	for remaining > 0 {
		want := int64(len(buf))
		if remaining < want {
			want = remaining
		}
		_ = rc.SetReadDeadline(time.Now().Add(SocketTimeout))
		n, err := r.Body.Read(buf[:want])
		remaining -= int64(n)
		if err != nil {
			break
		}
	}
	s.send(w, http.StatusBadRequest, errorBody(errMsg))
}

// receiveBody — тіло запиту у файл порціями; рядок — причина, чому не вийшло.
func (s *Server) receiveBody(w http.ResponseWriter, r *http.Request, part string, size int64) string {
	sink, err := os.Create(part)
	if err != nil {
		return fmt.Sprintf("upload failed: %v", err)
	}
	defer sink.Close()

	rc := http.NewResponseController(w)
	buf := make([]byte, uploadChunk)
	remaining := size
	for remaining > 0 {
		want := int64(len(buf))
		if remaining < want {
			want = remaining
		}
		got, err := fillChunk(rc, r.Body, buf[:want])
		if err != nil {
			return fmt.Sprintf("upload failed: %v", err)
		}
		if got == 0 {
			return "the connection dropped before the file was complete"
		}
		if _, err := sink.Write(buf[:got]); err != nil {
			return fmt.Sprintf("upload failed: %v", err)
		}
		remaining -= int64(got)
	}
	return ""
}

// fillChunk читає порцію цілком; дедлайн простою поновлюється на КОЖНОМУ read.
func fillChunk(rc *http.ResponseController, body io.Reader, buf []byte) (int, error) {
	got := 0
	for got < len(buf) {
		_ = rc.SetReadDeadline(time.Now().Add(SocketTimeout))
		n, err := body.Read(buf[got:])
		got += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				return got, nil
			}
			return got, err
		}
	}
	return got, nil
}

// --- превʼю відео ---

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request, rel string) {
	opened, ok := s.d.Media.OpenRanged(rel)
	if !ok {
		s.send(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	size := opened.Size
	start, end := int64(0), size-1
	partial := false
	if header := strings.TrimSpace(r.Header.Get("Range")); header != "" {
		if match := rangeRe.FindStringSubmatch(header); match != nil {
			first, last := match[1], match[2]
			if first != "" {
				limit := int64(0)
				if size != 0 {
					limit = size - 1
				}
				start = minInt64(pyParseUint(first), limit)
				if last != "" {
					end = minInt64(pyParseUint(last), size-1)
				}
			} else if last != "" {
				start = maxInt64(0, size-pyParseUint(last))
			}
			partial = true
			if start > end {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
		}
	}

	length := end - start + 1
	source, err := os.Open(opened.Path)
	if err != nil {
		s.send(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	defer source.Close()

	w.Header().Set("Content-Type", opened.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	code := http.StatusOK
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		code = http.StatusPartialContent
	}
	w.WriteHeader(code)
	if _, err := source.Seek(start, io.SeekStart); err != nil {
		return
	}
	// Обрив на перемотці/закритті превʼю — нормальний хід подій.
	_, _ = io.CopyBuffer(w, io.LimitReader(source, length), make([]byte, mediaChunk))
}

// --- /ws ---

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	key, ok := wsUpgradeRequested(r.Header)
	if !ok {
		s.send(w, http.StatusBadRequest, errorBody("expected a WebSocket upgrade"))
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.send(w, http.StatusInternalServerError, errorBody("cannot upgrade this connection"))
		return
	}
	raw, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer raw.Close()

	_ = raw.SetWriteDeadline(time.Now().Add(SocketTimeout))
	if _, err := raw.Write(wsHandshakeResponse(key)); err != nil {
		return
	}

	conn := &wsConn{raw: raw, r: buffered.Reader}
	s.d.Hub.Register(conn)
	defer func() {
		s.d.Hub.Unregister(conn)
		conn.sendClose()
	}()

	for {
		_ = raw.SetReadDeadline(time.Now().Add(wsSocketTimeout))
		if _, err := buffered.Reader.Peek(1); err != nil {
			if isTimeout(err) {
				continue
			}
			return
		}
		_ = raw.SetReadDeadline(time.Now().Add(wsSocketTimeout))
		opcode, payload, ok := decodeFrame(buffered.Reader)
		if !ok {
			return
		}
		switch opcode {
		case opClose:
			return
		case opPing:
			if err := conn.sendPong(payload); err != nil {
				return
			}
		case opText:
			s.handleCommand(conn, payload)
		}
	}
}

// --- дрібне ---

func (s *Server) send(w http.ResponseWriter, code int, body any) {
	payload := pyMarshal(body)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(code)
	// Відповідати може бути нікуди (типово — abort аплоаду), це не помилка.
	_, _ = w.Write(payload)
}

func (s *Server) sendFile(w http.ResponseWriter, name, contentType string, extra ...[2]string) {
	data, err := fs.ReadFile(s.d.Dashboard, name)
	if err != nil {
		s.send(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	for _, pair := range extra {
		w.Header().Set(pair[0], pair[1])
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) checkToken(query url.Values) bool {
	return DashboardTokenOK(query.Get("token"), s.d.Token)
}

func isLocalhost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host == "127.0.0.1" || host == "::1"
}

// pyContentLength — python int(headers.get("Content-Length", 0) or 0).
func pyContentLength(r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("Content-Length"))
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// pyParseUint — python int без межі розрядності: переповнення клампиться.
func pyParseUint(text string) int64 {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return math.MaxInt64
	}
	return n
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

type errorReply struct {
	Error string `json:"error"`
}

func errorBody(text string) errorReply { return errorReply{Error: text} }

type okReply struct {
	OK bool `json:"ok"`
}

func okBody() okReply { return okReply{OK: true} }

type uploadReply struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
}

func uploadedBody(rel string) uploadReply { return uploadReply{OK: true, Path: rel} }
