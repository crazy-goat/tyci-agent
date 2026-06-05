package debug

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type key struct{}

func NewContext(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, key{}, l)
}

func FromContext(ctx context.Context) *Logger {
	l, _ := ctx.Value(key{}).(*Logger)
	return l
}

type Logger struct {
	ID   string
	file *os.File
	mu   sync.Mutex
}

func Init() (*Logger, error) {
	id, err := newUUIDv7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}

	dir := filepath.Join(os.Getenv("HOME"), ".cache", "tyci-agent", "debug")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir debug: %w", err)
	}

	path := filepath.Join(dir, id+".log")
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create debug log: %w", err)
	}

	return &Logger{ID: id, file: f}, nil
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

func (l *Logger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return len(p), nil
	}
	return l.file.Write(p)
}

func (l *Logger) WriteRequest(method, url string, body []byte) {
	fmt.Fprintf(l, "--- REQUEST %s %s ---\n", method, url)
	l.Write(body)
	l.Write([]byte("\n"))
}

func (l *Logger) WriteResponse(status int, body []byte) {
	fmt.Fprintf(l, "--- RESPONSE %d ---\n", status)
	l.Write(body)
	l.Write([]byte("\n"))
}

func (l *Logger) WriteResponseLine(line []byte) {
	l.Write(line)
}

func (l *Logger) WriteRequestLine(prefix string, body []byte) {
	fmt.Fprintf(l, "--- %s ---\n", prefix)
	l.Write(body)
	l.Write([]byte("\n"))
}

func newUUIDv7() (string, error) {
	var buf [16]byte

	ts := uint64(time.Now().UnixMilli())
	buf[0] = byte(ts >> 40)
	buf[1] = byte(ts >> 32)
	buf[2] = byte(ts >> 24)
	buf[3] = byte(ts >> 16)
	buf[4] = byte(ts >> 8)
	buf[5] = byte(ts)

	if _, err := io.ReadFull(rand.Reader, buf[6:]); err != nil {
		return "", err
	}

	buf[6] = (buf[6] & 0x0f) | 0x70
	buf[8] = (buf[8] & 0x3f) | 0x80

	dst := make([]byte, 36)
	hex.Encode(dst[0:8], buf[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], buf[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], buf[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], buf[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], buf[10:16])
	return string(dst), nil
}
