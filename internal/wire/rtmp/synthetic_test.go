package rtmp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// Еталонні байти команд сесії.
const (
	goldenConnectPlain  = "020007636f6e6e656374003ff00000000000000300036170700200046170703200047479706502000a6e6f6e707269766174650008666c61736856657202001f464d4c452f332e302028636f6d70617469626c653b20464d53632f312e3029000673776655726c02001c72746d703a2f2f6578616d706c652e636f6d3a313933352f617070320005746355726c02001c72746d703a2f2f6578616d706c652e636f6d3a313933352f61707032000009"
	goldenConnectCodecs = "020007636f6e6e656374003ff00000000000000300036170700200046170703200047479706502000a6e6f6e707269766174650008666c61736856657202001f464d4c452f332e302028636f6d70617469626c653b20464d53632f312e3029000673776655726c02001c72746d70733a2f2f6578616d706c652e636f6d3a3434332f617070320005746355726c02001c72746d70733a2f2f6578616d706c652e636f6d3a3434332f61707032000a666f757243634c6973740a0000000202000461766331020004687663310012766964656f466f75724363496e666f4d61700300046176633100401c00000000000000046876633100401c000000000000000009000663617073457800401c000000000000000009"
	goldenRelease       = "02000d72656c6561736553747265616d0040000000000000000502000973747265616d6b6579"
	goldenFCPublish     = "02000946435075626c6973680040080000000000000502000973747265616d6b6579"
	goldenCreateStream  = "02000c63726561746553747265616d00401000000000000005"
	goldenPublish       = "0200077075626c6973680040140000000000000502000973747265616d6b65790200046c697665"
	goldenFCUnpublish   = "02000b4643556e7075626c6973680040180000000000000502000973747265616d6b6579"
	goldenDeleteStream  = "02000c64656c65746553747265616d00401c0000000000000500401c000000000000"
)

// fakeConn: читання з in, запис у out, дедлайни — no-op.
type fakeConn struct {
	in  *bytes.Reader
	out bytes.Buffer
}

func (f *fakeConn) Read(p []byte) (int, error) {
	if f.in == nil {
		return 0, io.EOF
	}
	return f.in.Read(p)
}
func (f *fakeConn) Write(p []byte) (int, error)      { return f.out.Write(p) }
func (f *fakeConn) Close() error                     { return nil }
func (f *fakeConn) LocalAddr() net.Addr              { return nil }
func (f *fakeConn) RemoteAddr() net.Addr             { return nil }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func newTestConn(t *testing.T, url string, codecs []string) (*Conn, *fakeConn) {
	t.Helper()
	c, err := NewConn("t", url, codecs)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeConn{}
	c.sock = fc
	return c, fc
}

// serverMsg — повідомлення сервера (fmt0, ts=0, продовження fmt3 без ext).
func serverMsg(csid, typeID byte, streamID uint32, payload []byte, chunk int) []byte {
	n := len(payload)
	out := []byte{csid, 0, 0, 0, byte(n >> 16), byte(n >> 8), byte(n), typeID,
		byte(streamID), byte(streamID >> 8), byte(streamID >> 16), byte(streamID >> 24)}
	k := min(n, chunk)
	out = append(out, payload[:k]...)
	rest := payload[k:]
	for len(rest) > 0 {
		out = append(out, byte(3<<6)|csid)
		k = min(len(rest), chunk)
		out = append(out, rest[:k]...)
		rest = rest[k:]
	}
	return out
}

type sentMsg struct {
	csid    byte
	typeID  byte
	stream  uint32
	ts      uint32
	payload []byte
}

// parseClientStream розбирає те, що емітить наш sendMessage (fmt0 +
// fmt3-продовження, ext-ts повторюється в продовженнях).
func parseClientStream(t *testing.T, data []byte, chunk int) []sentMsg {
	t.Helper()
	var msgs []sentMsg
	i := 0
	for i < len(data) {
		b0 := data[i]
		i++
		if b0>>6 != 0 {
			t.Fatalf("expected fmt0 basic header at offset %d, got %#02x", i-1, b0)
		}
		csid := b0 & 0x3F
		ts := uint32(data[i])<<16 | uint32(data[i+1])<<8 | uint32(data[i+2])
		i += 3
		n := int(data[i])<<16 | int(data[i+1])<<8 | int(data[i+2])
		i += 3
		typeID := data[i]
		i++
		stream := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
		i += 4
		ext := ts == 0xFFFFFF
		if ext {
			ts = beUint(data[i:i+4], 4)
			i += 4
		}
		k := min(n, chunk)
		payload := append([]byte(nil), data[i:i+k]...)
		i += k
		for len(payload) < n {
			if data[i] != byte(3<<6)|csid {
				t.Fatalf("expected fmt3 continuation at offset %d, got %#02x", i, data[i])
			}
			i++
			if ext {
				if got := beUint(data[i:i+4], 4); got != ts {
					t.Fatalf("continuation ext ts %d != %d", got, ts)
				}
				i += 4
			}
			k = min(n-len(payload), chunk)
			payload = append(payload, data[i:i+k]...)
			i += k
		}
		msgs = append(msgs, sentMsg{csid, typeID, stream, ts, payload})
	}
	return msgs
}

func TestParseURL(t *testing.T) {
	good := []struct {
		url, scheme, host, app, stream string
		port                           int
	}{
		{"rtmp://example.com/app2/streamkey", "rtmp", "example.com", "app2", "streamkey", 1935},
		{"rtmps://example.com/app2/streamkey", "rtmps", "example.com", "app2", "streamkey", 443},
		{"rtmp://host:1936/app/key", "rtmp", "host", "app", "key", 1936},
		{"rtmp://host/a/b/key", "rtmp", "host", "a/b", "key", 1935},
		{"rtmp://host/app/key?x=1", "rtmp", "host", "app", "key?x=1", 1935},
	}
	for _, g := range good {
		p, err := parsePushURL(g.url)
		if err != nil {
			t.Fatalf("%s: %v", g.url, err)
		}
		if p.scheme != g.scheme || p.host != g.host || p.port != g.port ||
			p.app != g.app || p.stream != g.stream {
			t.Fatalf("%s: got %+v", g.url, p)
		}
	}
	bad := []string{"", "http://x/y/z", "rtmp://host", "rtmp://host/onlyapp",
		"rtmp://host/app/", "rtmp://host//key", "rtmpt://host/app/key"}
	for _, u := range bad {
		if _, err := parsePushURL(u); err == nil {
			t.Fatalf("%q: expected error", u)
		}
	}
	if _, err := NewConn("t", "", nil); err == nil {
		t.Fatal("NewConn must surface an invalid URL error")
	}
}

func TestHandshakeBytes(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	s1 := make([]byte, 1536)
	for i := range s1 {
		s1[i] = byte(i)
	}
	fc.in = bytes.NewReader(concat([]byte{3}, s1, make([]byte, 1536)))
	if err := c.handshake(); err != nil {
		t.Fatal(err)
	}
	out := fc.out.Bytes()
	if len(out) != 1537+1536 {
		t.Fatalf("handshake wrote %d bytes", len(out))
	}
	if out[0] != 3 || !bytes.Equal(out[1:9], make([]byte, 8)) {
		t.Fatalf("bad C0/C1 prefix: % x", out[:12])
	}
	for i := 0; i < 1528; i++ { // C1 = детермінований патерн
		if out[9+i] != byte(i*7+3) {
			t.Fatalf("C1[%d] = %#02x, want %#02x", i, out[9+i], byte(i*7+3))
		}
	}
	if !bytes.Equal(out[1537:], s1) {
		t.Fatal("C2 must echo S1")
	}
	if c.bytesIn != 3073 { // S0+S1+S2 рахуються в ack-лічильник (Q13)
		t.Fatalf("bytesIn after handshake = %d, want 3073", c.bytesIn)
	}
}

func TestSendMessageChunking(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	c.outChunk = 16
	payload := make([]byte, 40)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	if err := c.sendMessage(6, 9, 1, payload, 100); err != nil {
		t.Fatal(err)
	}
	want := concat(
		[]byte{0x06, 0, 0, 100, 0, 0, 40, 9, 1, 0, 0, 0},
		payload[:16], []byte{0xC6}, payload[16:32], []byte{0xC6}, payload[32:])
	if !bytes.Equal(fc.out.Bytes(), want) {
		t.Fatalf("chunked frame mismatch:\ngot  % x\nwant % x", fc.out.Bytes(), want)
	}
	msgs := parseClientStream(t, fc.out.Bytes(), 16)
	if len(msgs) != 1 || msgs[0].ts != 100 || !bytes.Equal(msgs[0].payload, payload) {
		t.Fatalf("reassembly mismatch: %+v", msgs)
	}
}

func TestSendMessageExtendedTimestamp(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	c.outChunk = 8
	payload := make([]byte, 20)
	for i := range payload {
		payload[i] = byte(0xA0 + i)
	}
	const ts = 0x01234567
	if err := c.sendMessage(4, 8, 1, payload, ts); err != nil {
		t.Fatal(err)
	}
	ext := []byte{0x01, 0x23, 0x45, 0x67}
	want := concat(
		[]byte{0x04, 0xFF, 0xFF, 0xFF, 0, 0, 20, 8, 1, 0, 0, 0}, ext,
		payload[:8], []byte{0xC4}, ext, payload[8:16], []byte{0xC4}, ext, payload[16:])
	if !bytes.Equal(fc.out.Bytes(), want) {
		t.Fatalf("ext-ts frame mismatch:\ngot  % x\nwant % x", fc.out.Bytes(), want)
	}
	// Межа: 0xFFFFFF-1 ще без ext, 0xFFFFFF вже з ext.
	fc.out.Reset()
	if err := c.sendMessage(4, 8, 1, []byte{1}, 0xFFFFFE); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fc.out.Bytes(), []byte{0x04, 0xFF, 0xFF, 0xFE, 0, 0, 1, 8, 1, 0, 0, 0, 1}) {
		t.Fatalf("0xFFFFFE frame mismatch: % x", fc.out.Bytes())
	}
	fc.out.Reset()
	if err := c.sendMessage(4, 8, 1, []byte{1}, 0xFFFFFF); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fc.out.Bytes(),
		[]byte{0x04, 0xFF, 0xFF, 0xFF, 0, 0, 1, 8, 1, 0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 1}) {
		t.Fatalf("0xFFFFFF frame mismatch: % x", fc.out.Bytes())
	}
}

func TestWriteTagSemantics(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	meta := concat(onMetaDataPrefix, []byte{0x08, 0, 0, 0, 0, 0, 0, 0x09})
	audio := []byte{0xAF, 1, 0xDE, 0xAD}
	video := []byte{0x17, 1, 0, 0, 0, 0xBE, 0xEF}
	for _, w := range []struct {
		typ byte
		ts  uint32
		p   []byte
	}{{18, 0, meta}, {8, 10, audio}, {9, 11, video}, {18, 12, meta}, {99, 13, audio}} {
		if err := c.WriteTag(w.typ, int64(w.ts), w.p); err != nil {
			t.Fatal(err)
		}
	}
	msgs := parseClientStream(t, fc.out.Bytes(), c.outChunk)
	if len(msgs) != 5 {
		t.Fatalf("got %d messages", len(msgs))
	}
	wrapped := concat(amfString("@setDataFrame"), meta)
	// csid: audio->4, data->5, video->6, невідомий тип->4; всі на stream_id.
	if msgs[0].csid != 5 || msgs[0].typeID != 18 || !bytes.Equal(msgs[0].payload, wrapped) {
		t.Fatalf("onMetaData not wrapped once: %+v", msgs[0])
	}
	if msgs[1].csid != 4 || !bytes.Equal(msgs[1].payload, audio) {
		t.Fatalf("audio altered: %+v", msgs[1])
	}
	if msgs[2].csid != 6 || !bytes.Equal(msgs[2].payload, video) {
		t.Fatalf("video altered: %+v", msgs[2])
	}
	// Q11: обгортка per-tag — другий onMetaData теж обгортається.
	if !bytes.Equal(msgs[3].payload, wrapped) {
		t.Fatalf("second onMetaData: %+v", msgs[3])
	}
	if msgs[4].csid != 4 {
		t.Fatalf("unknown tag type csid = %d, want 4", msgs[4].csid)
	}
	for _, m := range msgs {
		if m.stream != 1 {
			t.Fatalf("stream id = %d, want 1", m.stream)
		}
	}

	c.Close()
	if err := c.WriteTag(8, 20, audio); !errors.Is(err, ErrDead) {
		t.Fatalf("WriteTag on dead conn: %v", err)
	}
}

func TestConnectObjectGolden(t *testing.T) {
	c1, _ := newTestConn(t, "rtmp://example.com/app2/streamkey", nil)
	got := hex.EncodeToString(concat(amfString("connect"), amfNumber(1), c1.connectObject()))
	if got != goldenConnectPlain {
		t.Fatalf("plain connect differs from oracle:\ngot  %s\nwant %s", got, goldenConnectPlain)
	}
	c2, _ := newTestConn(t, "rtmps://example.com/app2/streamkey", []string{"avc1", "hvc1"})
	got = hex.EncodeToString(concat(amfString("connect"), amfNumber(1), c2.connectObject()))
	if got != goldenConnectCodecs {
		t.Fatalf("codecs connect differs from oracle:\ngot  %s\nwant %s", got, goldenConnectCodecs)
	}
}

func TestSessionCommandGoldens(t *testing.T) {
	cases := []struct {
		name, golden string
		payload      []byte
	}{
		{"releaseStream", goldenRelease,
			concat(amfString("releaseStream"), amfNumber(2), amfNull(), amfString("streamkey"))},
		{"FCPublish", goldenFCPublish,
			concat(amfString("FCPublish"), amfNumber(3), amfNull(), amfString("streamkey"))},
		{"createStream", goldenCreateStream,
			concat(amfString("createStream"), amfNumber(4), amfNull())},
		{"publish", goldenPublish,
			concat(amfString("publish"), amfNumber(5), amfNull(), amfString("streamkey"), amfString("live"))},
	}
	for _, cs := range cases {
		if got := hex.EncodeToString(cs.payload); got != cs.golden {
			t.Fatalf("%s differs from oracle:\ngot  %s\nwant %s", cs.name, got, cs.golden)
		}
	}
}

func TestTeardownGolden(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/streamkey", nil)
	c.streamID = 7
	c.Teardown()
	msgs := parseClientStream(t, fc.out.Bytes(), c.outChunk)
	if len(msgs) != 2 {
		t.Fatalf("teardown sent %d messages", len(msgs))
	}
	if got := hex.EncodeToString(msgs[0].payload); got != goldenFCUnpublish {
		t.Fatalf("FCUnpublish differs from oracle:\ngot %s", got)
	}
	if got := hex.EncodeToString(msgs[1].payload); got != goldenDeleteStream {
		t.Fatalf("deleteStream differs from oracle:\ngot %s", got)
	}
	if c.IsAlive() {
		t.Fatal("conn must be dead after teardown")
	}
	// Повторний teardown по мертвому — жодних нових повідомлень.
	fc.out.Reset()
	c.Teardown()
	if fc.out.Len() != 0 {
		t.Fatalf("dead teardown sent %d bytes", fc.out.Len())
	}
}

func TestReadMessageReassembly(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	big := make([]byte, 300)
	for i := range big {
		big[i] = byte(i * 3)
	}
	fc.in = bytes.NewReader(concat(
		serverMsg(3, typeCmdAMF0, 0, big, 128),
		serverMsg(2, typeSetChunkSize, 0, be32(4096), 128),
		serverMsg(3, typeCmdAMF0, 0, big, 4096),
	))
	mtype, payload, err := c.readMessage(nil)
	if err != nil || mtype != typeCmdAMF0 || !bytes.Equal(payload, big) {
		t.Fatalf("msg1: type %d err %v equal %v", mtype, err, bytes.Equal(payload, big))
	}
	mtype, payload, err = c.readMessage(nil)
	if err != nil || mtype != typeSetChunkSize {
		t.Fatalf("msg2: type %d err %v", mtype, err)
	}
	if handled, err := c.handleControl(mtype, payload); !handled || err != nil {
		t.Fatalf("set chunk size unhandled: %v", err)
	}
	if c.inChunk != 4096 {
		t.Fatalf("inChunk = %d", c.inChunk)
	}
	mtype, payload, err = c.readMessage(nil)
	if err != nil || mtype != typeCmdAMF0 || !bytes.Equal(payload, big) {
		t.Fatalf("msg3 after chunk-size change: type %d err %v", mtype, err)
	}
}

func TestReadMessageHeaderFormats(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	p10 := bytes.Repeat([]byte{0x11}, 10)
	p5a := []byte{1, 2, 3, 4, 5}
	p5b := []byte{6, 7, 8, 9, 10}
	p5c := []byte{11, 12, 13, 14, 15}
	var in []byte
	in = append(in, serverMsg(4, typeAudio, 1, p10, 128)...)
	in = append(in, 0x40|4, 0, 0, 10, 0, 0, 5, typeAudio) // fmt1: delta 10, len 5
	in = append(in, p5a...)
	in = append(in, 0x80|4, 0, 0, 10) // fmt2: delta 10
	in = append(in, p5b...)
	in = append(in, 0xC0|4) // fmt3
	in = append(in, p5c...)
	fc.in = bytes.NewReader(in)
	for i, want := range [][]byte{p10, p5a, p5b, p5c} {
		mtype, payload, err := c.readMessage(nil)
		if err != nil || mtype != typeAudio || !bytes.Equal(payload, want) {
			t.Fatalf("msg %d: type %d err %v payload % x", i, mtype, err, payload)
		}
	}
	if st := c.st(4); st.ts != 30 { // 0 +10 +10 +10
		t.Fatalf("tracked ts = %d, want 30", st.ts)
	}
}

func TestAckCounter(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	body := bytes.Repeat([]byte{0xEE}, 30)
	fc.in = bytes.NewReader(concat(
		serverMsg(2, typeWindowAck, 0, be32(100), 128), // 16 байт на проводі
		serverMsg(3, typeAudio, 1, body, 128),          // 42 байти
		serverMsg(3, typeAudio, 1, body, 128),
		serverMsg(3, typeAudio, 1, body, 128),
	))
	for i := 0; i < 4; i++ {
		mtype, payload, err := c.readMessage(nil)
		if err != nil {
			t.Fatalf("msg %d: %v", i, err)
		}
		if _, err := c.handleControl(mtype, payload); err != nil {
			t.Fatal(err)
		}
	}
	if c.ackWindow != 100 {
		t.Fatalf("ackWindow = %d", c.ackWindow)
	}
	// bytesIn по повідомленнях: 16, 58, 100, 142; поріг window/2=50 ->
	// ack на 58 і 142.
	want := concat(
		serverMsg(2, typeAck, 0, be32(58), 4096),
		serverMsg(2, typeAck, 0, be32(142), 4096),
	)
	if !bytes.Equal(fc.out.Bytes(), want) {
		t.Fatalf("acks mismatch:\ngot  % x\nwant % x", fc.out.Bytes(), want)
	}
}

func TestWindowAckZeroKeepsOld(t *testing.T) {
	c, _ := newTestConn(t, "rtmp://example.com/app/key", nil)
	if _, err := c.handleControl(typeWindowAck, be32(0)); err != nil {
		t.Fatal(err)
	}
	if c.ackWindow != 2_500_000 { // Q14: нульове вікно не приймається
		t.Fatalf("ackWindow = %d", c.ackWindow)
	}
	if _, err := c.handleControl(typeWindowAck, []byte{0, 1}); err != nil {
		t.Fatal(err) // короткий payload ігнорується (len < 4)
	}
	if c.ackWindow != 2_500_000 {
		t.Fatalf("ackWindow = %d", c.ackWindow)
	}
}

func TestPingPong(t *testing.T) {
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	handled, err := c.handleControl(typeUserControl, []byte{0, ucPingRequest, 0xAA, 0xBB, 0xCC, 0xDD})
	if !handled || err != nil {
		t.Fatalf("ping unhandled: %v", err)
	}
	want := serverMsg(2, typeUserControl, 0, []byte{0, ucPingResponse, 0xAA, 0xBB, 0xCC, 0xDD}, 4096)
	if !bytes.Equal(fc.out.Bytes(), want) {
		t.Fatalf("pong mismatch:\ngot  % x\nwant % x", fc.out.Bytes(), want)
	}
}

func TestWaitCommandFlow(t *testing.T) {
	result := concat(amfString("_result"), amfNumber(1), amfNull(),
		amfObject([]amfField{{"code", amfString("NetConnection.Connect.Success")}}))
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	fc.in = bytes.NewReader(concat(
		serverMsg(2, typeWindowAck, 0, be32(250_000_000), 128),
		serverMsg(2, typeSetPeerBW, 0, append(be32(250_000_000), 2), 128),
		serverMsg(2, typeSetChunkSize, 0, be32(4096), 128),
		serverMsg(3, typeCmdAMF0, 0, result, 4096),
	))
	vals, err := c.waitCommand("_result")
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := vals[0].(string); name != "_result" {
		t.Fatalf("vals[0] = %v", vals[0])
	}
	if c.ackWindow != 250_000_000 || c.inChunk != 4096 {
		t.Fatalf("controls not applied: window %d chunk %d", c.ackWindow, c.inChunk)
	}
	info, ok := vals[3].(map[string]any)
	if !ok || info["code"] != "NetConnection.Connect.Success" {
		t.Fatalf("info = %v", vals[3])
	}

	// Fail-onStatus, поки чекаємо _result -> відмова.
	failStatus := concat(amfString("onStatus"), amfNumber(0), amfNull(),
		amfObject([]amfField{{"code", amfString("NetStream.Publish.Failed")}}))
	c2, fc2 := newTestConn(t, "rtmp://example.com/app/key", nil)
	fc2.in = bytes.NewReader(serverMsg(3, typeCmdAMF0, 0, failStatus, 4096))
	if _, err := c2.waitCommand("_result"); err == nil ||
		!bytes.Contains([]byte(err.Error()), []byte("server rejected publish")) {
		t.Fatalf("expected rejection, got %v", err)
	}

	// _error -> відмова.
	amfErr := concat(amfString("_error"), amfNumber(1), amfNull())
	c3, fc3 := newTestConn(t, "rtmp://example.com/app/key", nil)
	fc3.in = bytes.NewReader(serverMsg(3, typeCmdAMF0, 0, amfErr, 4096))
	if _, err := c3.waitCommand("_result"); err == nil {
		t.Fatal("expected rejection on _error")
	}

	// Q15: очікуваний onStatus із Fail-кодом — це відмова, а не публікація.
	c4, fc4 := newTestConn(t, "rtmp://example.com/app/key", nil)
	fc4.in = bytes.NewReader(serverMsg(3, typeCmdAMF0, 0, failStatus, 4096))
	if _, err := c4.waitCommand("onStatus"); err == nil ||
		!bytes.Contains([]byte(err.Error()), []byte("server rejected publish")) {
		t.Fatalf("expected rejection of failed publish, got %v", err)
	}

	okStatus := concat(amfString("onStatus"), amfNumber(0), amfNull(),
		amfObject([]amfField{{"code", amfString("NetStream.Publish.Start")}}))
	c5, fc5 := newTestConn(t, "rtmp://example.com/app/key", nil)
	fc5.in = bytes.NewReader(serverMsg(3, typeCmdAMF0, 0, okStatus, 4096))
	if _, err := c5.waitCommand("onStatus"); err != nil {
		t.Fatalf("accepted publish must pass: %v", err)
	}
}

// Q15: _result чужої команди не сходить за очікуваний.
func TestWaitCommandMatchesTransactionID(t *testing.T) {
	result := func(txn float64, sid float64) []byte {
		return concat(amfString("_result"), amfNumber(txn), amfNull(), amfNumber(sid))
	}
	c, fc := newTestConn(t, "rtmp://example.com/app/key", nil)
	fc.in = bytes.NewReader(concat(
		serverMsg(3, typeCmdAMF0, 0, result(2, 99), 4096), // releaseStream
		serverMsg(3, typeCmdAMF0, 0, result(4, 7), 4096),  // createStream
	))
	vals, err := c.waitCommandTxn(4, "_result")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := vals[3].(float64); got != 7 {
		t.Fatalf("stream id from the wrong _result: %v", vals[3])
	}

	// Сервер без transaction id лишається прийнятним.
	c2, fc2 := newTestConn(t, "rtmp://example.com/app/key", nil)
	fc2.in = bytes.NewReader(serverMsg(3, typeCmdAMF0, 0, result(0, 3), 4096))
	if _, err := c2.waitCommandTxn(4, "_result"); err != nil {
		t.Fatalf("txn-less reply must pass: %v", err)
	}
}

func TestAmfDecode(t *testing.T) {
	// Обрізаний рядок клампиться як py-зріз і ПОВЕРТАЄТЬСЯ.
	if vals := amfReadAll(amfString("hello")[:6]); len(vals) != 1 || vals[0] != "hel" {
		t.Fatalf("clamped string: %v", vals)
	}
	// Обрізаний number відкидається.
	if vals := amfReadAll(amfNumber(5)[:6]); len(vals) != 0 {
		t.Fatalf("truncated number: %v", vals)
	}
	// ECMA array (0x08) читається як object.
	ecma := concat([]byte{0x08, 0, 0, 0, 1},
		[]byte{0, 1, 'k'}, amfNumber(2), []byte{0, 0, 0x09})
	vals := amfReadAll(concat(ecma, amfString("tail")))
	if len(vals) != 2 {
		t.Fatalf("ecma parse: %v", vals)
	}
	if m, ok := vals[0].(map[string]any); !ok || m["k"] != 2.0 {
		t.Fatalf("ecma map: %v", vals[0])
	}
	if vals[1] != "tail" {
		t.Fatalf("tail: %v", vals[1])
	}
	// Невідомий тип зупиняє розбір, зібране лишається.
	if vals := amfReadAll(concat(amfString("a"), []byte{0x0C, 1, 2})); len(vals) != 1 || vals[0] != "a" {
		t.Fatalf("unsupported type: %v", vals)
	}
	// bool + null.
	if vals := amfReadAll([]byte{0x01, 0x01, 0x05}); len(vals) != 2 || vals[0] != true || vals[1] != nil {
		t.Fatalf("bool/null: %v", vals)
	}
}

func TestMidMessageTimeoutIsFatal(t *testing.T) {
	cli, srv := net.Pipe()
	defer srv.Close()
	c, err := NewConn("t", "rtmp://example.com/app/key", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.sock = cli
	c.rwTimeout = 80 * time.Millisecond
	go func() {
		// Заголовок обіцяє 10 байт, віддано 3 — далі тиша.
		msg := serverMsg(3, typeCmdAMF0, 0, bytes.Repeat([]byte{7}, 10), 128)
		_, _ = srv.Write(msg[:12+3])
	}()
	_, _, err = c.readMessage(nil)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("mid-message stall must be a deadline error, got %v", err)
	}
}

func TestReaderLifecycle(t *testing.T) {
	cli, srv := net.Pipe()
	defer srv.Close()
	c, err := NewConn("t", "rtmp://example.com/app/key", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.sock = cli
	c.readTick = 20 * time.Millisecond
	c.rwTimeout = 500 * time.Millisecond
	go c.reader()

	time.Sleep(100 * time.Millisecond) // кілька тиків без даних — не смерть
	if !c.IsAlive() {
		t.Fatal("idle ticks must not kill the connection")
	}

	ping := serverMsg(2, typeUserControl, 0, []byte{0, ucPingRequest, 1, 2, 3, 4}, 128)
	if _, err := srv.Write(ping); err != nil {
		t.Fatal(err)
	}
	pong := make([]byte, 18)
	if _, err := io.ReadFull(srv, pong); err != nil {
		t.Fatal(err)
	}
	wantPong := serverMsg(2, typeUserControl, 0, []byte{0, ucPingResponse, 1, 2, 3, 4}, 4096)
	if !bytes.Equal(pong, wantPong) {
		t.Fatalf("pong mismatch:\ngot  % x\nwant % x", pong, wantPong)
	}
	if !c.IsAlive() {
		t.Fatal("alive after ping/pong")
	}

	fail := concat(amfString("onStatus"), amfNumber(0), amfNull(),
		amfObject([]amfField{{"code", amfString("NetStream.Publish.Failed")}}))
	if _, err := srv.Write(serverMsg(3, typeCmdAMF0, 0, fail, 128)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not die on server Fail status")
	}
}

func TestReaderDiesOnServerClose(t *testing.T) {
	cli, srv := net.Pipe()
	c, err := NewConn("t", "rtmp://example.com/app/key", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.sock = cli
	c.readTick = 20 * time.Millisecond
	go c.reader()
	time.Sleep(50 * time.Millisecond)
	srv.Close()
	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not die on closed connection")
	}
}
