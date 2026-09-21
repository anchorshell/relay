package body

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var ErrTooLarge = errors.New("request body too large")

type Handle struct {
	size int64
	data []byte
	path string
	once sync.Once
}

func (h *Handle) Size() int64 {
	return h.size
}

func (h *Handle) Open() (io.ReadCloser, error) {
	if h.path != "" {
		return os.Open(filepath.Clean(h.path))
	}
	return io.NopCloser(bytes.NewReader(h.data)), nil
}

func (h *Handle) Cleanup() error {
	var err error
	h.once.Do(func() {
		if h.path != "" {
			err = os.Remove(filepath.Clean(h.path))
		}
	})
	return err
}

// ReleaseMemory drops the in-memory copy after an optional queued-body store
// has durably accepted it. File-backed OSS handles are unaffected.
func (h *Handle) ReleaseMemory() {
	if h == nil || h.path != "" {
		return
	}
	for index := range h.data {
		h.data[index] = 0
	}
	h.data = nil
}

type Store struct {
	tempDir      string
	memoryMax    int64
	spoolAtBytes int64
	maxBytes     int64
}

func NewStore(tempDir string, memoryMax, spoolAtBytes int64) *Store {
	return &Store{
		tempDir:      tempDir,
		memoryMax:    memoryMax,
		spoolAtBytes: spoolAtBytes,
	}
}

func (s *Store) WithMaxBytes(maxBytes int64) *Store {
	s.maxBytes = maxBytes
	return s
}

func (s *Store) Save(ctx context.Context, source io.Reader) (*Handle, error) {
	var buf bytes.Buffer
	var tmp *os.File
	cleanup := func() {
		if tmp == nil {
			return
		}
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
	}

	var total int64
	chunk := make([]byte, 32*1024)
	memoryMax := s.memoryMax
	if memoryMax <= 0 {
		memoryMax = s.spoolAtBytes
	}
	for {
		select {
		case <-ctx.Done():
			cleanup()
			return nil, ctx.Err()
		default:
		}

		n, err := source.Read(chunk)
		if n > 0 {
			total += int64(n)
			if s.maxBytes > 0 && total > s.maxBytes {
				cleanup()
				return nil, ErrTooLarge
			}
			if memoryMax <= 0 || total <= memoryMax {
				if _, werr := buf.Write(chunk[:n]); werr != nil {
					cleanup()
					return nil, werr
				}
			} else {
				if tmp == nil {
					tmp, err = os.CreateTemp(s.tempDir, "relay-body-*")
					if err != nil {
						return nil, err
					}
					if _, werr := tmp.Write(buf.Bytes()); werr != nil {
						cleanup()
						return nil, werr
					}
					buf.Reset()
				}
				if buf.Len() > 0 {
					cleanup()
					return nil, fmt.Errorf("buffer should be empty after spill")
				}
				if _, werr := tmp.Write(chunk[:n]); werr != nil {
					cleanup()
					return nil, werr
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			return nil, err
		}
	}

	if tmp == nil {
		return &Handle{size: total, data: append([]byte(nil), buf.Bytes()...)}, nil
	}
	defer func() {
		_ = tmp.Close()
	}()

	return &Handle{size: total, path: tmp.Name()}, nil
}

func (s *Store) TempPath(handle *Handle) string {
	if handle == nil {
		return ""
	}
	return fmt.Sprintf("%s", handle.path)
}
