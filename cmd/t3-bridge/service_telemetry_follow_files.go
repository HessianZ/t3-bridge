package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func followServiceTelemetryFiles(ctx context.Context, dir string, lines int, checkDir func(string) error, output io.Writer, account receiverProcessAccount) error {
	paths := [...]string{filepath.Join(dir, "stdout.log"), filepath.Join(dir, "stderr.log")}
	var streams [len(paths)]serviceTelemetryFile
	buffer := make([]byte, serviceLogMaxBytes)
	encoder := json.NewEncoder(output)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for initial := true; ctx.Err() == nil; initial = false {
		err := checkDir(dir)
		switch {
		case err == nil:
			for i, path := range paths {
				if err := streams[i].poll(path, initial, lines, buffer, encoder, account); err != nil {
					return fmt.Errorf("follow %s: %w", filepath.Base(path), err)
				}
			}
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

type serviceTelemetryFile struct {
	info    os.FileInfo
	offset  int64
	partial []byte
	discard bool
}

func (stream *serviceTelemetryFile) poll(path string, initial bool, lines int, buffer []byte, encoder *json.Encoder, account receiverProcessAccount) error {
	file, err := localfile.OpenRegular(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if stream.info != nil && (!os.SameFile(stream.info, info) || size < stream.info.Size()) {
		*stream = serviceTelemetryFile{}
	}
	stream.info = info
	start := stream.offset
	if initial {
		start = max(0, size-int64(len(buffer)))
		if lines == 0 {
			start = max(0, size-1)
		}
	}
	data := buffer[:min(size-start, int64(len(buffer)))]
	n, err := file.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return err
	}
	if n < len(data) {
		// The file shrank during this read. Start over on the next poll.
		*stream = serviceTelemetryFile{}
		return nil
	}
	stream.offset = start + int64(n)
	if initial {
		if lines == 0 {
			stream.discard = len(data) != 0 && data[0] != '\n'
			return nil
		}
		if start > 0 {
			// Use one byte of the window to establish its first line boundary.
			boundary := data[0] == '\n'
			data = data[1:]
			if !boundary {
				_, rest, found := bytes.Cut(data, []byte{'\n'})
				data, stream.discard = rest, !found
			}
		}
		data = serviceLogTail(data, lines)
	}
	return stream.consume(data, encoder, account)
}

func (stream *serviceTelemetryFile) consume(data []byte, encoder *json.Encoder, account receiverProcessAccount) error {
	for len(data) > 0 {
		piece, rest, complete := bytes.Cut(data, []byte{'\n'})
		if !stream.discard {
			// The existing size limit includes the terminating newline.
			if len(stream.partial)+len(piece) >= serviceTelemetryMaxLineBytes {
				stream.discard = true
			} else {
				stream.partial = append(stream.partial, piece...)
			}
		}
		if !complete {
			break
		}
		if !stream.discard {
			if record, ok := parseServiceTelemetry(stream.partial); ok && record.matchesAccount(account) {
				if err := encoder.Encode(record); err != nil {
					return err
				}
			}
		}
		stream.partial = stream.partial[:0]
		stream.discard = false
		data = rest
	}
	return nil
}
