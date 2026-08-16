// Package rtmp — нативний RTMP(S)-push для виходів, чиє фреймування ffmpeg
// не переживе (0x95-аудіо хімери, Ex-відео EB-драбини): handshake, чанкинг,
// AMF0-мінімум, connect/createStream/publish, кожен FLV-тег — RTMP-повідомлення
// з payload-ом як є. Порт протокольної частини (_PushConnection);
// супервізор RtmpPushClient портується окремо в egress.
package rtmp

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ConnectTimeout = 10 * time.Second
	// Аналог -rw_timeout виходів: блокуючий I/O довше цього = зависле з'єднання.
	RWTimeout = 15 * time.Second
)

const (
	typeSetChunkSize = 1
	typeAck          = 3
	typeUserControl  = 4
	typeWindowAck    = 5
	typeSetPeerBW    = 6
	typeAudio        = 8
	typeVideo        = 9
	typeDataAMF0     = 18
	typeCmdAMF0      = 20

	ucPingRequest  = 6
	ucPingResponse = 7
)

var onMetaDataPrefix = []byte("\x02\x00\x0aonMetaData")

// ErrDead — WriteTag по мертвому з'єднанню (аналог BrokenPipeError).
var ErrDead = errors.New("rtmp push connection is dead")

func tagCSID(tagType byte) byte {
	switch tagType {
	case typeAudio:
		return 4
	case typeDataAMF0:
		return 5
	case typeVideo:
		return 6
	}
	return 4
}

type chunkState struct {
	ts, delta uint32
	mlen      int
	mtype     byte
	stream    uint32
	buf       []byte
	remaining int
}

// Conn — одне живе з'єднання: setup (handshake/connect/createStream/publish),
// reader-горутина (дренаж сервера, відповіді на ping), запис тегів. Смерть
// (обрив/зависання/teardown) закриває Dead, на який чекає супервізор.
type Conn struct {
	name           string
	announceCodecs []string
	scheme         string
	host           string
	app            string
	streamName     string
	port           int

	sock      net.Conn
	streamID  uint32
	outChunk  int
	inChunk   int
	csidState map[uint32]*chunkState

	sendMu   sync.Mutex
	deadOnce sync.Once
	deadCh   chan struct{}

	// Acknowledgement (spec 5.4): type-3 кожні window/2 прийнятих байт (як
	// librtmp SendBytesReceived). Publisher приймає мало, але строгий ingest
	// вправі чекати ack.
	ackWindow    int
	bytesIn      int
	bytesInAcked int

	connectTimeout time.Duration // = ConnectTimeout/RWTimeout/1с; поля — для тестів
	rwTimeout      time.Duration
	readTick       time.Duration
}

// NewConn парсить URL і готує з'єднання; невалідний URL — помилка, яку
// викликач зобов'язаний побачити (грабля: мовчки мертвий потік супервізора).
func NewConn(name, url string, announceCodecs []string) (*Conn, error) {
	t, err := parsePushURL(url)
	if err != nil {
		return nil, err
	}
	return &Conn{
		name:           name,
		announceCodecs: append([]string(nil), announceCodecs...),
		scheme:         t.scheme,
		host:           t.host,
		app:            t.app,
		streamName:     t.stream,
		port:           t.port,
		streamID:       1,
		outChunk:       4096,
		inChunk:        128,
		csidState:      map[uint32]*chunkState{},
		deadCh:         make(chan struct{}),
		ackWindow:      2_500_000,
		connectTimeout: ConnectTimeout,
		rwTimeout:      RWTimeout,
		readTick:       time.Second,
	}, nil
}

// Host — для логів супервізора ("connecting to...").
func (c *Conn) Host() string { return c.host }

// --- інтерфейс OutputSink ---

// WriteHeader — no-op: RTMP-повідомлення не потребують FLV-заголовка файлу.
func (c *Conn) WriteHeader() error { return nil }

// WriteTag шле один FLV-тег як RTMP-повідомлення; маска шкали до 32 біт — тут.
func (c *Conn) WriteTag(tagType byte, ts int64, payload []byte) error {
	if !c.IsAlive() {
		return ErrDead
	}
	// ffmpeg-FLV дає голий onMetaData; publisher-конвенція RTMP — обгортка
	// @setDataFrame (так шле і OBS, і ffmpeg-мукcер).
	if tagType == typeDataAMF0 && bytes.HasPrefix(payload, onMetaDataPrefix) {
		payload = append(amfString("@setDataFrame"), payload...)
	}
	if err := c.sendMessage(tagCSID(tagType), tagType, c.streamID, payload, uint32(ts)); err != nil {
		c.markDead("send failed: " + err.Error())
		return err
	}
	return nil
}

// --- низький рівень ---

func (c *Conn) send(b []byte) error {
	_ = c.sock.SetWriteDeadline(time.Now().Add(c.rwTimeout))
	_, err := c.sock.Write(b)
	return err
}

func (c *Conn) recvExact(n int) ([]byte, error) {
	buf := make([]byte, n)
	for got := 0; got < n; {
		_ = c.sock.SetReadDeadline(time.Now().Add(c.rwTimeout))
		m, err := c.sock.Read(buf[got:])
		got += m
		if got == n {
			break
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("server closed connection")
			}
			return nil, err
		}
	}
	c.bytesIn += n
	return buf, nil
}

func (c *Conn) maybeSendAck() error {
	if c.bytesIn-c.bytesInAcked >= c.ackWindow/2 {
		c.bytesInAcked = c.bytesIn
		return c.sendMessage(2, typeAck, 0, be32(uint32(c.bytesIn)), 0)
	}
	return nil
}

func (c *Conn) handshake() error {
	c0c1 := make([]byte, 1+8+1528)
	c0c1[0] = 3
	for i := 0; i < 1528; i++ {
		c0c1[9+i] = byte(i*7 + 3)
	}
	if err := c.send(c0c1); err != nil {
		return err
	}
	if _, err := c.recvExact(1); err != nil { // S0
		return err
	}
	s1, err := c.recvExact(1536)
	if err != nil {
		return err
	}
	if _, err := c.recvExact(1536); err != nil { // S2
		return err
	}
	return c.send(s1) // C2 = echo S1
}

func (c *Conn) sendMessage(csid, typeID byte, streamID uint32, payload []byte, ts uint32) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	var ts3 [3]byte
	var ext []byte
	if ts >= 0xFFFFFF {
		ts3 = [3]byte{0xFF, 0xFF, 0xFF}
		ext = be32(ts)
	} else {
		ts3 = [3]byte{byte(ts >> 16), byte(ts >> 8), byte(ts)}
	}
	n := len(payload)
	out := make([]byte, 0, 16+n+(n/c.outChunk+1)*(1+len(ext)))
	out = append(out, csid) // fmt 0; csid тут завжди < 64
	out = append(out, ts3[:]...)
	out = append(out, byte(n>>16), byte(n>>8), byte(n))
	out = append(out, typeID)
	out = append(out, byte(streamID), byte(streamID>>8), byte(streamID>>16), byte(streamID>>24))
	out = append(out, ext...)
	first := min(n, c.outChunk)
	out = append(out, payload[:first]...)
	rest := payload[first:]
	bh3 := byte(3<<6) | csid
	for len(rest) > 0 {
		out = append(out, bh3)
		out = append(out, ext...) // ext timestamp повторюється в type-3 чанках
		k := min(len(rest), c.outChunk)
		out = append(out, rest[:k]...)
		rest = rest[k:]
	}
	return c.send(out)
}

func (c *Conn) st(csid uint32) *chunkState {
	s := c.csidState[csid]
	if s == nil {
		s = &chunkState{}
		c.csidState[csid] = s
	}
	return s
}

// readMessage збирає одне повне повідомлення сервера; pre — вже прочитаний
// перший байт (тик reader-а), або nil.
func (c *Conn) readMessage(pre *byte) (byte, []byte, error) {
	for {
		var b0 byte
		if pre != nil {
			b0, pre = *pre, nil
		} else {
			b, err := c.recvExact(1)
			if err != nil {
				return 0, nil, err
			}
			b0 = b[0]
		}
		hfmt := b0 >> 6
		csid := uint32(b0 & 0x3F)
		switch csid {
		case 0:
			b, err := c.recvExact(1)
			if err != nil {
				return 0, nil, err
			}
			csid = 64 + uint32(b[0])
		case 1:
			b, err := c.recvExact(2)
			if err != nil {
				return 0, nil, err
			}
			csid = 64 + uint32(b[0]) + uint32(b[1])*256
		}
		st := c.st(csid)
		switch hfmt {
		case 0:
			h, err := c.recvExact(11)
			if err != nil {
				return 0, nil, err
			}
			ts := beUint(h[0:3], 3)
			st.mlen = int(beUint(h[3:6], 3))
			st.mtype = h[6]
			st.stream = uint32(h[7]) | uint32(h[8])<<8 | uint32(h[9])<<16 | uint32(h[10])<<24
			if ts == 0xFFFFFF {
				e, err := c.recvExact(4)
				if err != nil {
					return 0, nil, err
				}
				ts = beUint(e, 4)
			}
			st.ts, st.delta = ts, 0
			st.remaining, st.buf = st.mlen, nil
		case 1:
			h, err := c.recvExact(7)
			if err != nil {
				return 0, nil, err
			}
			delta := beUint(h[0:3], 3)
			st.mlen = int(beUint(h[3:6], 3))
			st.mtype = h[6]
			if delta == 0xFFFFFF {
				e, err := c.recvExact(4)
				if err != nil {
					return 0, nil, err
				}
				delta = beUint(e, 4)
			}
			st.ts += delta
			st.delta = delta
			st.remaining, st.buf = st.mlen, nil
		case 2:
			h, err := c.recvExact(3)
			if err != nil {
				return 0, nil, err
			}
			delta := beUint(h, 3)
			if delta == 0xFFFFFF {
				e, err := c.recvExact(4)
				if err != nil {
					return 0, nil, err
				}
				delta = beUint(e, 4)
			}
			st.ts += delta
			st.delta = delta
			st.remaining, st.buf = st.mlen, nil
		default: // fmt 3
			if st.remaining == 0 {
				st.ts += st.delta
				st.remaining, st.buf = st.mlen, nil
			}
		}
		want := min(st.remaining, c.inChunk)
		data, err := c.recvExact(want)
		if err != nil {
			return 0, nil, err
		}
		st.buf = append(st.buf, data...)
		st.remaining -= want
		if err := c.maybeSendAck(); err != nil {
			return 0, nil, err
		}
		if st.remaining == 0 && st.mlen > 0 {
			return st.mtype, st.buf, nil
		}
	}
}

func (c *Conn) handleControl(mtype byte, payload []byte) (bool, error) {
	switch mtype {
	case typeSetChunkSize:
		c.inChunk = int(beUint(payload, 4))
	case typeWindowAck:
		if len(payload) >= 4 {
			if w := int(beUint(payload, 4)); w != 0 {
				c.ackWindow = w
			}
		}
	case typeAck, typeSetPeerBW:
	case typeUserControl:
		if beUint(payload, 2) == ucPingRequest {
			resp := concat([]byte{0, ucPingResponse}, pySlice(payload, 2, len(payload)))
			if err := c.sendMessage(2, typeUserControl, 0, resp, 0); err != nil {
				return true, err
			}
		}
	default:
		return false, nil
	}
	return true, nil
}

func (c *Conn) waitCommand(names ...string) ([]any, error) {
	return c.waitCommandTxn(0, names...)
}

// waitCommandTxn — чекає одну з команд names; txn != 0 вимагає ще й збігу
// transaction id.
func (c *Conn) waitCommandTxn(txn float64, names ...string) ([]any, error) {
	for {
		mtype, payload, err := c.readMessage(nil)
		if err != nil {
			return nil, err
		}
		handled, err := c.handleControl(mtype, payload)
		if err != nil {
			return nil, err
		}
		if handled {
			continue
		}
		if mtype != typeCmdAMF0 {
			continue
		}
		vals := amfReadAll(payload)
		name := ""
		if len(vals) > 0 {
			name, _ = vals[0].(string)
		}
		// Fail-код б'є навіть по очікуваній команді.
		if name == "_error" || (name == "onStatus" && strings.Contains(statusCode(vals), "Fail")) {
			return nil, fmt.Errorf("server rejected publish: %v", vals)
		}
		matched := false
		for _, want := range names {
			if name == want {
				matched = true
				break
			}
		}
		if matched && replyTxnMatches(vals, txn) {
			return vals, nil
		}
	}
}

// replyTxnMatches — чужа відповідь (наприклад _result на releaseStream) не
// сходить за очікувану; сервер без transaction id пропускається.
func replyTxnMatches(vals []any, txn float64) bool {
	if txn == 0 || len(vals) < 2 {
		return true
	}
	got, ok := vals[1].(float64)
	if !ok || got == 0 {
		return true
	}
	return got == txn
}

// statusCode — str(info.get("code", "")) першого dict-а серед значень.
func statusCode(vals []any) string {
	for _, v := range vals {
		if m, ok := v.(map[string]any); ok {
			if code, ok := m["code"]; ok {
				return fmt.Sprint(code)
			}
			return ""
		}
	}
	return ""
}

// --- сесія ---

// ConnectAndPublish: TCP/TLS + handshake + connect/createStream/publish;
// після прийнятого publish стартує reader-горутина. На помилці сокет НЕ
// закривається — викликач кличе Close (контракт супервізора, як у Python).
func (c *Conn) ConnectAndPublish() error {
	d := net.Dialer{Timeout: c.connectTimeout}
	raw, err := d.Dial("tcp", net.JoinHostPort(c.host, strconv.Itoa(c.port)))
	if err != nil {
		return err
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	if c.scheme == "rtmps" {
		tlsConn := tls.Client(raw, &tls.Config{ServerName: c.host})
		_ = tlsConn.SetDeadline(time.Now().Add(c.connectTimeout))
		if err := tlsConn.Handshake(); err != nil {
			_ = raw.Close()
			return err
		}
		_ = tlsConn.SetDeadline(time.Time{})
		c.sock = tlsConn
	} else {
		c.sock = raw
	}
	if err := c.handshake(); err != nil {
		return err
	}
	if err := c.sendMessage(2, typeSetChunkSize, 0, be32(uint32(c.outChunk)), 0); err != nil {
		return err
	}

	if err := c.sendMessage(3, typeCmdAMF0, 0,
		concat(amfString("connect"), amfNumber(1), c.connectObject()), 0); err != nil {
		return err
	}
	if _, err := c.waitCommandTxn(1, "_result"); err != nil {
		return err
	}

	if err := c.sendMessage(3, typeCmdAMF0, 0,
		concat(amfString("releaseStream"), amfNumber(2), amfNull(), amfString(c.streamName)), 0); err != nil {
		return err
	}
	if err := c.sendMessage(3, typeCmdAMF0, 0,
		concat(amfString("FCPublish"), amfNumber(3), amfNull(), amfString(c.streamName)), 0); err != nil {
		return err
	}
	if err := c.sendMessage(3, typeCmdAMF0, 0,
		concat(amfString("createStream"), amfNumber(4), amfNull()), 0); err != nil {
		return err
	}
	// _result саме на createStream, не на releaseStream/FCPublish.
	vals, err := c.waitCommandTxn(4, "_result")
	if err != nil {
		return err
	}
	sid := 1.0
	if len(vals) > 2 {
		for _, v := range vals[2:] {
			if f, ok := v.(float64); ok {
				sid = f
				break
			}
		}
	}
	c.streamID = uint32(int64(sid))

	if err := c.sendMessage(4, typeCmdAMF0, c.streamID,
		concat(amfString("publish"), amfNumber(5), amfNull(),
			amfString(c.streamName), amfString("live")), 0); err != nil {
		return err
	}
	if _, err := c.waitCommand("onStatus"); err != nil {
		return err
	}
	log.Printf("rtmp-push[%s] publish accepted by %s", c.name, c.host)
	go c.reader()
	return nil
}

func (c *Conn) connectObject() []byte {
	tcURL := c.scheme + "://" + c.host + ":" + strconv.Itoa(c.port) + "/" + c.app
	// FMLE-connect без fourCcList/capsEx: OBS шле 0x95-аудіо-хімеру без
	// узгодження можливостей, і саме так вона перевірена наживо.
	fields := []amfField{
		{"app", amfString(c.app)},
		{"type", amfString("nonprivate")},
		{"flashVer", amfString("FMLE/3.0 (compatible; FMSc/1.0)")},
		{"swfUrl", amfString(tcURL)},
		{"tcUrl", amfString(tcURL)},
	}
	if len(c.announceCodecs) > 0 {
		// enhanced-rtmp-v2: FourCcInfoMask CanDecode|CanEncode|CanForward,
		// capsEx Reconnect|Multitrack|ModEx. Мультитрек-ВІДЕО без цієї
		// декларації відхиляється тихо (онлайн-сокет, офлайн-трансляція);
		// з нею та сама помилка стає явним розривом, який бачить супервізор.
		items := make([][]byte, 0, len(c.announceCodecs))
		var infoMap []amfField
		seen := map[string]bool{}
		for _, cc := range c.announceCodecs {
			items = append(items, amfString(cc))
			if !seen[cc] { // python-dict: дублікати ключів колапсують
				seen[cc] = true
				infoMap = append(infoMap, amfField{cc, amfNumber(0x07)})
			}
		}
		fields = append(fields,
			amfField{"fourCcList", amfStrictArray(items)},
			amfField{"videoFourCcInfoMap", amfObject(infoMap)},
			amfField{"capsEx", amfNumber(0x07)},
		)
	}
	return amfObject(fields)
}

// reader: тик перед читанням — сервер може мовчати як завгодно довго, а
// socket-timeout ПОСЕРЕД повідомлення = реально мертве з'єднання, не retry.
func (c *Conn) reader() {
	for c.IsAlive() {
		var tick [1]byte
		_ = c.sock.SetReadDeadline(time.Now().Add(c.readTick))
		if _, err := io.ReadFull(c.sock, tick[:]); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			c.markDead(fmt.Sprintf("server connection lost: %v", err))
			return
		}
		c.bytesIn++
		mtype, payload, err := c.readMessage(&tick[0])
		if err != nil {
			c.markDead(fmt.Sprintf("server connection lost: %v", err))
			return
		}
		handled, err := c.handleControl(mtype, payload)
		if err != nil {
			c.markDead(fmt.Sprintf("server connection lost: %v", err))
			return
		}
		if handled {
			continue
		}
		if mtype == typeCmdAMF0 {
			vals := amfReadAll(payload)
			log.Printf("rtmp-push[%s] server says: %v", c.name, vals)
			name := ""
			if len(vals) > 0 {
				name, _ = vals[0].(string)
			}
			if name == "_error" || strings.Contains(statusCode(vals), "Fail") {
				c.markDead(fmt.Sprintf("server error: %v", vals))
				return
			}
		}
	}
}

// --- життєвий цикл ---

func (c *Conn) markDead(reason string) {
	c.deadOnce.Do(func() {
		log.Printf("rtmp-push[%s] %s", c.name, reason)
		close(c.deadCh)
	})
}

func (c *Conn) IsAlive() bool {
	select {
	case <-c.deadCh:
		return false
	default:
		return true
	}
}

// Dead закривається у мить смерті з'єднання (обрив/зависання/teardown).
func (c *Conn) Dead() <-chan struct{} { return c.deadCh }

// WaitDead блокується до смерті з'єднання.
func (c *Conn) WaitDead() { <-c.deadCh }

// Teardown — штатне завершення: FCUnpublish/deleteStream best-effort + close.
func (c *Conn) Teardown() {
	if c.IsAlive() {
		if err := c.sendMessage(3, typeCmdAMF0, 0,
			concat(amfString("FCUnpublish"), amfNumber(6), amfNull(),
				amfString(c.streamName)), 0); err == nil {
			_ = c.sendMessage(3, typeCmdAMF0, 0,
				concat(amfString("deleteStream"), amfNumber(7), amfNull(),
					amfNumber(float64(c.streamID))), 0)
		}
	}
	c.Close()
}

// Close позначає з'єднання мертвим (без лога) і закриває сокет.
func (c *Conn) Close() {
	c.deadOnce.Do(func() { close(c.deadCh) })
	if c.sock != nil {
		_ = c.sock.Close()
	}
}

// --- дрібні хелпери ---

// pySlice — кламп меж зрізу як у Python.
func pySlice(b []byte, from, to int) []byte {
	if from > len(b) {
		from = len(b)
	}
	if to > len(b) {
		to = len(b)
	}
	if to < from {
		to = from
	}
	return b[from:to]
}

// beUint — int.from_bytes(b[:n], "big"): бере скільки є, як py-зріз.
func beUint(b []byte, n int) uint32 {
	if n > len(b) {
		n = len(b)
	}
	var v uint32
	for _, x := range b[:n] {
		v = v<<8 | uint32(x)
	}
	return v
}

func be32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
