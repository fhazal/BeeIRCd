package server

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"
)

// sinkConn is a net.Conn that throws away what is written.
type sinkConn struct{ net.Conn }

func (sinkConn) Write(p []byte) (int, error)      { return len(p), nil }
func (sinkConn) Close() error                     { return nil }
func (sinkConn) SetWriteDeadline(time.Time) error { return nil }
func (sinkConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }

// Whatever bytes a page sends, reading never panics or makes up data.
func FuzzWebSocketRead(f *testing.F) {
	f.Add([]byte{0x81, 0x84, 1, 2, 3, 4, 'P' ^ 1, 'I' ^ 2, 'N' ^ 3, 'G' ^ 4})
	f.Add([]byte{0x01, 0x81, 0, 0, 0, 0, 'a', 0x89, 0x80, 0, 0, 0, 0, 0x80, 0x81, 0, 0, 0, 0, 'b'})
	f.Add([]byte{0x81, 0xfe, 0x00, 0x05, 0, 0, 0, 0, 'a', '\r', '\n', 'b', 'c'})
	f.Add([]byte{0x88, 0x82, 0, 0, 0, 0, 0x03, 0xe8})
	f.Fuzz(func(t *testing.T, in []byte) {
		w := &wsConn{Conn: sinkConn{}, r: bufio.NewReader(bytes.NewReader(in)), text: true}
		total := 0
		buf := make([]byte, 512)
		for {
			n, err := w.Read(buf)
			if total += n; total > len(in)+2 {
				t.Fatalf("read %d bytes from %d sent", total, len(in))
			}
			if err != nil {
				return
			}
		}
	})
}
