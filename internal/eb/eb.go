// Package eb — Twitch Enhanced Broadcasting go-live-обмін і звірка виданої
// драбини зі спостереженою.
package eb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	goliveURL      = "https://ingest.twitch.tv/api/v3/GetClientConfiguration"
	requestTimeout = 15 * time.Second
	schemaVersion  = "2025-01-25"
	service        = "IVS"
)

// transport — шов для тестів (фейк-RoundTripper замість live Twitch), той
// шов для тестів замість реального транспорту.
var transport http.RoundTripper = http.DefaultTransport

// Rung — спостережена на проводі відеосходинка (з паспорта source).
type Rung struct {
	Width  int
	Height int
	Fps    int // 0 = невідомо (VUI без timing info)
}

// GrantedRung — сходинка виданої Twitch-ом драбини.
type GrantedRung struct {
	Width       int
	Height      int
	Fps         int // 0 = не звіряти (пара до Rung.Fps)
	BitrateKbps int // 0 = не показувати у format
}

// Session — результат успішного go-live-обміну.
type Session struct {
	URL   string
	Rungs []GrantedRung
}

func canvas(width, height, fps int) jObj {
	return jObj{
		{"canvas_height", height},
		{"canvas_width", width},
		{"framerate", jObj{
			{"denominator", 1},
			{"numerator", fps},
		}},
		{"height", height},
		{"width", width},
	}
}

// buildPreferences — порт build_preferences: канвас = верхня спостережена
// сходинка ЗА ПЛОЩЕЮ (перша на рівних площах), maximum_video_tracks —
// кількість спостережених сходинок.
func buildPreferences(rungs []Rung, vodTrackAudio bool) jObj {
	top := rungs[0]
	topArea := top.Width * top.Height
	for _, r := range rungs[1:] {
		if area := r.Width * r.Height; area > topArea {
			top, topArea = r, area
		}
	}
	fps := top.Fps
	if fps == 0 {
		fps = 60
	}
	return jObj{
		{"audio_channels", 2},
		{"audio_fixed_buffering", false},
		{"audio_max_buffering_ms", 960},
		{"audio_samples_per_sec", 48000},
		{"composition_gpu_index", 0},
		{"canvases", jArr{canvas(top.Width, top.Height, fps)}},
		{"maximum_video_tracks", len(rungs)},
		{"vod_track_audio", vodTrackAudio},
	}
}

func buildRequest(streamKey string, preferences jObj) jObj {
	return jObj{
		{"authentication", streamKey},
		{"capabilities", capabilitiesJSON},
		{"client", clientJSON},
		{"preferences", preferences},
		{"schema_version", schemaVersion},
		{"service", service},
	}
}

// FormatRungs — порт format_rungs.
func FormatRungs(rungs []GrantedRung) string {
	if len(rungs) == 0 {
		return "(empty)"
	}
	parts := make([]string, len(rungs))
	for i, r := range rungs {
		fpsStr := "?"
		if r.Fps != 0 {
			fpsStr = strconv.Itoa(r.Fps)
		}
		s := fmt.Sprintf("%dx%d@%s", r.Width, r.Height, fpsStr)
		if r.BitrateKbps != 0 {
			s += fmt.Sprintf("/%dk", r.BitrateKbps)
		}
		parts[i] = s
	}
	return strings.Join(parts, ", ")
}

// CompareLadder — порт compare_ladder: звірка ПОЗИЦІЙНА (доріжка i мусить
// відповідати сходинці i), "" == немає розбіжності (пара до TL1: sentinel
// замість *string, реальне повідомлення ніколи не порожнє).
func CompareLadder(observed []Rung, granted []GrantedRung) string {
	if len(granted) == 0 {
		return "Twitch granted an empty ladder"
	}
	if len(observed) != len(granted) {
		return fmt.Sprintf("the source carries %d video track(s), Twitch granted %d rung(s): %s",
			len(observed), len(granted), FormatRungs(granted))
	}
	for i, seen := range observed {
		rung := granted[i]
		if seen.Width != rung.Width || seen.Height != rung.Height {
			return fmt.Sprintf("video track #%d is %dx%d, Twitch granted %dx%d at that position",
				i+1, seen.Width, seen.Height, rung.Width, rung.Height)
		}
		if seen.Fps != 0 && rung.Fps != 0 && seen.Fps != rung.Fps {
			return fmt.Sprintf("video track #%d runs at %d fps, Twitch granted %d fps at that position",
				i+1, seen.Fps, rung.Fps)
		}
	}
	return ""
}

// dget/dlist/dfloat/dstring —.get-семантика Python dict над розібраним
// JSON (map[string]any): відсутній ключ/чужий тип мовчки дають нуль-значення.
func dget(v any, key string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m[key]
}

func dlist(v any, key string) []any {
	l, _ := dget(v, key).([]any)
	return l
}

func dfloat(v any, key string) float64 {
	f, _ := dget(v, key).(float64)
	return f
}

func dstring(v any, key string) string {
	s, _ := dget(v, key).(string)
	return s
}

// grantedRungs — порт granted_rungs. Порядок відповіді зберігається:
// збігається з trackId на проводі.
func grantedRungs(response any) []GrantedRung {
	var rungs []GrantedRung
	for _, entry := range dlist(response, "encoder_configurations") {
		framerate := dget(entry, "framerate")
		den := dfloat(framerate, "denominator")
		if den == 0 {
			den = 1
		}
		fps := int(math.RoundToEven(dfloat(framerate, "numerator") / den))
		rungs = append(rungs, GrantedRung{
			Width:       int(dfloat(entry, "width")),
			Height:      int(dfloat(entry, "height")),
			Fps:         fps,
			BitrateKbps: int(dfloat(dget(entry, "settings"), "bitrate")),
		})
	}
	return rungs
}

// selectEndpoint — порт fetch_session-у частини вибору RTMP/RTMPS
// ingest_endpoints: RTMP переважає RTMPS, url_template/authentication мають
// бути непорожні.
func selectEndpoint(response any) (urlTemplate, auth string, ok bool) {
	endpoints := dlist(response, "ingest_endpoints")
	pick := func(protocol string) (any, bool) {
		for _, e := range endpoints {
			if dstring(e, "protocol") == protocol {
				return e, true
			}
		}
		return nil, false
	}
	endpoint, found := pick("RTMP")
	if !found {
		endpoint, found = pick("RTMPS")
	}
	if !found {
		return "", "", false
	}
	urlTemplate, auth = dstring(endpoint, "url_template"), dstring(endpoint, "authentication")
	if urlTemplate == "" || auth == "" {
		return "", "", false
	}
	return urlTemplate, auth, true
}

// postGolive — порт post_golive: POST + розбір/розпізнавання трьох класів
// помилок (HTTP-статус, транспорт, читання/парсинг тіла).
func postGolive(body jObj) (any, error) {
	req, err := http.NewRequest(http.MethodPost, goliveURL, bytes.NewReader([]byte(pyDumps(body))))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "libobs")

	client := &http.Client{Transport: transport, Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the Twitch go-live API: %s", transportErrorReason(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Twitch rejected the go-live request: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("malformed reply from the Twitch go-live API: %s", err)
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("malformed reply from the Twitch go-live API: %s", err)
	}
	return parsed, nil
}

func transportErrorReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

// FetchSession — порт fetch_session: звичайний ключ платформи -> виданий
// EB-ендпойнт + драбина. compare_ladder проти спостереженого — окремий
// виклик на боці споживача (Manager.eb_push_url), не тут.
func FetchSession(streamKey string, rungs []Rung, vodTrackAudio bool) (*Session, error) {
	if strings.TrimSpace(streamKey) == "" {
		return nil, errors.New("no stream key configured for this platform")
	}
	if len(rungs) == 0 {
		return nil, errors.New("no video ladder observed on the source yet")
	}
	response, err := postGolive(buildRequest(streamKey, buildPreferences(rungs, vodTrackAudio)))
	if err != nil {
		return nil, err
	}
	granted := grantedRungs(response)
	if len(granted) == 0 {
		return nil, errors.New("Twitch granted no renditions -- Enhanced Broadcasting is probably not enabled for this channel")
	}
	urlTemplate, auth, ok := selectEndpoint(response)
	if !ok {
		return nil, errors.New("the go-live reply carried no usable RTMP ingest endpoint")
	}
	configID := dget(dget(response, "meta"), "config_id")
	log.Printf("twitch go-live: granted ladder %s (config_id=%v)", FormatRungs(granted), configID)
	return &Session{URL: strings.ReplaceAll(urlTemplate, "{stream_key}", auth), Rungs: granted}, nil
}
