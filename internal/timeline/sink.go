package timeline

import (
	"sync"

	"restream_go/internal/wire/flv"
)

// Глибина черги виходу (у тегах, ~70 тег/с при 30fps+AAC). Primary терпиміший
// до заторів, restream — коротший, щоб швидше ресинкнутись на свіжий keyframe.
const (
	PrimaryQueueMax  = 900
	RestreamQueueMax = 300
)

// Скільки секунд після останнього дропу вважати вихід «відстаючим».
const behindWindowSec = 3.0

// SinkOutput — байтовий вихід однієї платформи (flv.PipeOutput, ts.MuxOutput,
// egress.PushConn).
type SinkOutput interface {
	WriteHeader() error
	WriteTag(tagType byte, ts int64, payload []byte) error
}

// Sink — те, що таймлайн вимагає від виходу; реалізація — *OutputSink.
type Sink interface {
	Name() string
	Offer(tagType byte, ts int64, payload []byte)
}

// SinkStats — health-метрики виходу для Control.
type SinkStats struct {
	Dropped int
	Behind  bool
}

type queuedTag struct {
	tagType byte
	ts      int64
	payload []byte
}

// OutputSink — один вихід (одна платформа): власна черга + горутина-писар.
// Offer ніколи не блокує викликача; FLV-заголовок і seq-header sink інжектить
// собі сам на першому keyframe свого поточного з'єднання.
type OutputSink struct {
	name   string
	maxlen int
	clock  Clock

	mu          sync.Mutex
	cv          *sync.Cond
	queue       []queuedTag
	out         SinkOutput
	seedHeaders map[string][]byte
	// Кожне (пере)підключення виходу піднімає gen — писар помічає це й
	// починає з чистого аркуша.
	gen        int
	overflow   bool
	dropped    int
	lastDropAt float64
	closed     bool
	done       chan struct{}
}

// NewOutputSink створює вихід і запускає його горутину-писар.
func NewOutputSink(name string, isPrimary bool, clk Clock) *OutputSink {
	maxlen := RestreamQueueMax
	if isPrimary {
		maxlen = PrimaryQueueMax
	}
	if clk == nil {
		clk = SystemClock()
	}
	s := &OutputSink{
		name:        name,
		maxlen:      maxlen,
		clock:       clk,
		seedHeaders: map[string][]byte{},
		done:        make(chan struct{}),
	}
	s.cv = sync.NewCond(&s.mu)
	go s.run()
	return s
}

func (s *OutputSink) Name() string { return s.name }

// Attach підключає свіжий вихідний потік (stdin нового ffmpeg-процесу).
func (s *OutputSink) Attach(out SinkOutput, seedHeaders map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = out
	s.seedHeaders = copyHeaders(seedHeaders)
	s.gen++
	s.queue = nil
	s.cv.Signal()
}

// Detach ідемпотентний: процес виходу завершився — більше нічого не пишемо.
func (s *OutputSink) Detach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = nil
	s.gen++
	s.queue = nil
	s.cv.Signal()
}

// Close остаточно зупиняє писаря (вихід видаляють зовсім).
func (s *OutputSink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cv.Signal()
}

func (s *OutputSink) Offer(tagType byte, ts int64, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out == nil {
		return
	}
	if len(s.queue) >= s.maxlen {
		// Платформа не встигає: застарілий хвіст лише збільшує відставання,
		// тож чистимо чергу й ресинкнемось на наступному keyframe.
		s.dropped += len(s.queue) // теги, не події переповнення
		s.queue = nil
		s.overflow = true
		s.lastDropAt = s.clock.Now()
	}
	s.queue = append(s.queue, queuedTag{tagType, ts, payload})
	s.cv.Signal()
}

func (s *OutputSink) Stats() SinkStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	behind := s.lastDropAt > 0 && (s.clock.Now()-s.lastDropAt) < behindWindowSec
	return SinkStats{Dropped: s.dropped, Behind: behind}
}

func (s *OutputSink) run() {
	defer close(s.done)
	var (
		item          queuedTag
		out           SinkOutput
		started       bool
		startedTracks = map[byte]bool{}
		headers       = map[string][]byte{}
		localGen      int
	)
	for {
		s.mu.Lock()
		for {
			if s.closed {
				s.mu.Unlock()
				return
			}
			if s.gen != localGen {
				// Чергу тут НЕ чистимо: attach/detach уже зробили це під
				// локом, а теги, що надійшли ПІСЛЯ attach (той самий gen),
				// валідні — повторна чистка вимила б їх.
				localGen = s.gen
				out = s.out
				headers = copyHeaders(s.seedHeaders)
				started = false
				startedTracks = map[byte]bool{}
			}
			if s.overflow {
				s.overflow = false
				started = false
				startedTracks = map[byte]bool{}
			}
			if len(s.queue) > 0 {
				item = s.queue[0]
				s.queue = s.queue[1:]
				break
			}
			s.cv.Wait()
		}
		outNow := out
		s.mu.Unlock()
		if outNow == nil {
			continue
		}
		started = forwardTag(outNow, item, started, headers, startedTracks)
	}
}

// forwardTag віддає один канонічний тег у вихід; headers/startedTracks
// мутуються на місці, повертається новий started.
func forwardTag(out SinkOutput, item queuedTag, started bool, headers map[string][]byte, startedTracks map[byte]bool) bool {
	seq := flv.IsSeqHeader(item.tagType, item.payload)
	isMeta := item.tagType == flv.TagScript
	if seq {
		headers[flv.HeaderKey(item.tagType, item.payload)] = item.payload
	} else if isMeta {
		headers["meta"] = item.payload
	}

	if seq || isMeta {
		// Поки вихід не стартував — лише кешуємо (інжектимо перед першим
		// keyframe нижче); стартованому форвардимо одразу.
		if started {
			safeWrite(out, item.tagType, item.ts, item.payload)
		}
		return started
	}

	isVideo := item.tagType == flv.TagVideo
	isKF := isVideo && flv.IsVideoKeyframe(item.payload)
	var trackID byte
	if isVideo {
		trackID = flv.VideoTrackID(item.payload)
	}
	if !started {
		// Стартуємо лише з keyframe КАНОНІЧНОЇ доріжки (legacy, track 0):
		// keyframe сходинки драбини сам по собі потік придатним не робить.
		if !(isKF && trackID == 0) {
			return started
		}
		safeWriteHeader(out)
		for _, h := range flv.OrderedHeaderItems(headers) {
			safeWrite(out, h.TagType, item.ts, headers[h.Key])
		}
		started = true
		startedTracks[0] = true
	} else if isVideo && !startedTracks[trackID] {
		if !isKF {
			return started // ця сходинка чекає власного keyframe
		}
		startedTracks[trackID] = true
	}
	safeWrite(out, item.tagType, item.ts, item.payload)
	return started
}

// Смерть виходу сигналізує його процес/з'єднання, не sink — тут помилки запису
// свідомо ковтаються.
func safeWrite(out SinkOutput, tagType byte, ts int64, payload []byte) {
	_ = out.WriteTag(tagType, ts, payload)
}

func safeWriteHeader(out SinkOutput) {
	_ = out.WriteHeader()
}

func copyHeaders(src map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
