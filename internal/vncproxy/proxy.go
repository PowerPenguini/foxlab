package vncproxy

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func ProxyRawTCP(w http.ResponseWriter, r *http.Request, target string) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeProxyError(w, fmt.Errorf("console endpoint expects a websocket upgrade"), http.StatusUpgradeRequired)
		return
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		writeProxyError(w, fmt.Errorf("server does not support hijacking"), http.StatusInternalServerError)
		return
	}
	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		writeProxyError(w, err, http.StatusBadGateway)
		return
	}
	client, rw, err := h.Hijack()
	if err != nil {
		_ = conn.Close()
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		_ = client.Close()
		_ = conn.Close()
		return
	}
	accept := websocketAccept(key)
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.WriteString("Upgrade: websocket\r\n")
	_, _ = rw.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	_ = rw.Flush()

	go func() {
		defer client.Close()
		defer conn.Close()
		_ = websocketToTCP(rw.Reader, conn)
	}()
	go func() {
		defer client.Close()
		defer conn.Close()
		_ = tcpToWebsocket(conn, client)
	}()
}

func writeProxyError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`+"\n", err.Error())
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func websocketToTCP(reader *bufio.Reader, target net.Conn) error {
	for {
		opcode, payload, err := readWebsocketFrame(reader)
		if err != nil {
			return err
		}
		switch opcode {
		case 0x1, 0x2, 0x0:
			if len(payload) > 0 {
				if _, err := target.Write(payload); err != nil {
					return err
				}
			}
		case 0x8:
			return nil
		case 0x9:
			continue
		}
	}
}

func tcpToWebsocket(src net.Conn, client net.Conn) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if err := writeWebsocketFrame(client, 0x2, buf[:n]); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
}

func readWebsocketFrame(r *bufio.Reader) (byte, []byte, error) {
	header, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	opcode := header & 0x0f
	lenByte, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	masked := lenByte&0x80 != 0
	payloadLen := uint64(lenByte & 0x7f)
	switch payloadLen {
	case 126:
		var raw [2]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(raw[:]))
	case 127:
		var raw [8]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(raw[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	if payloadLen > 16*1024*1024 {
		return 0, nil, fmt.Errorf("websocket frame too large")
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeWebsocketFrame(w io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
