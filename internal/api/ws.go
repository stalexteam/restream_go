package api

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Опкоди RFC 6455.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// maxFramePayload — більший фрейм розриває з'єднання.
const maxFramePayload = 1 << 20

// wsSocketTimeout — settimeout на сокеті /ws після хендшейку.
const wsSocketTimeout = time.Second

// wsAccept — Sec-WebSocket-Accept для ключа клієнта.
func wsAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// wsHandshakeResponse — байти відповіді 101.
func wsHandshakeResponse(key string) []byte {
	return []byte("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAccept(key) + "\r\n" +
		"\r\n")
}

// wsUpgradeRequested — заголовки схожі на WS-хендшейк.
func wsUpgradeRequested(h http.Header) (key string, ok bool) {
	upgrade := strings.ToLower(h.Get("Upgrade"))
	connection := strings.ToLower(h.Get("Connection"))
	key = h.Get("Sec-WebSocket-Key")
	if upgrade != "websocket" || !strings.Contains(connection, "upgrade") || key == "" {
		return "", false
	}
	return key, true
}

// encodeFrame — серверний (неmasked) фрейм: 7/16/64-бітна довжина.
func encodeFrame(opcode byte, payload []byte) []byte {
	length := len(payload)
	var header []byte
	switch {
	case length < 126:
		header = []byte{0x80 | opcode, byte(length)}
	case length < 65536:
		header = []byte{0x80 | opcode, 126, 0, 0}
		binary.BigEndian.PutUint16(header[2:], uint16(length))
	default:
		header = make([]byte, 10)
		header[0] = 0x80 | opcode
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(length))
	}
	return append(header, payload...)
}

// decodeFrame читає один фрейм клієнта. ok=false — з'єднання закрите (EOF,
// зокрема посеред фрейму), завеликий фрейм або помилка читання.
func decodeFrame(r io.Reader) (opcode byte, payload []byte, ok bool) {
	header, ok := readExact(r, 2)
	if !ok {
		return 0, nil, false
	}
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)

	switch length {
	case 126:
		ext, ok := readExact(r, 2)
		if !ok {
			return 0, nil, false
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext, ok := readExact(r, 8)
		if !ok {
			return 0, nil, false
		}
		length = binary.BigEndian.Uint64(ext)
	}

	if length > maxFramePayload {
		return 0, nil, false
	}

	var maskKey []byte
	if masked {
		maskKey, ok = readExact(r, 4)
		if !ok {
			return 0, nil, false
		}
	}

	payload = []byte{}
	if length > 0 {
		payload, ok = readExact(r, int(length))
		if !ok {
			return 0, nil, false
		}
	}
	if masked && len(payload) > 0 {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, true
}

func readExact(r io.Reader, n int) ([]byte, bool) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, false
	}
	return buf, true
}

// wsConn — одне /ws-з'єднання: сирий сокет плюс власний лок запису (
// — `threading.Lock` на з'єднання, який роздає hub).
type wsConn struct {
	raw net.Conn
	r   io.Reader
	mu  sync.Mutex
}

// writeFrame — фрейм під локом запису з дедлайном сокета.
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	frame := encodeFrame(opcode, payload)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.raw.SetWriteDeadline(time.Now().Add(wsSocketTimeout)); err != nil {
		return err
	}
	_, err := c.raw.Write(frame)
	return err
}

func (c *wsConn) sendText(text []byte) error { return c.writeFrame(opText, text) }

func (c *wsConn) sendPong(payload []byte) error { return c.writeFrame(opPong, payload) }

// sendClose ковтає помилку запису — сокет уже може бути мертвим.
func (c *wsConn) sendClose() { _ = c.writeFrame(opClose, []byte{}) }
