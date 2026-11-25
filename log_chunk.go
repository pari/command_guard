package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Ranged reads of a log file for the RunBook step log panel, so multi-MB logs
// are shown a window at a time instead of being sent to the browser whole.
const (
	logChunkMinBytes     = 1024
	logChunkMaxBytes     = 1024 * 1024
	logChunkDefaultBytes = 128 * 1024
	logChunkHeaderProbe  = 64 * 1024
)

type logChunk struct {
	Content  []byte
	Offset   int64
	End      int64
	Total    int64
	Fields   map[string]string
	Storage  string
	S3URI    string
	Modified time.Time
}

// isLogChunkRequest reports whether the caller asked for a window of the log.
func isLogChunkRequest(query url.Values) bool {
	return query.Has("bytes") || query.Has("before")
}

// parseLogChunkParams reads bytes (window size) and before (end offset, -1 = end of file).
func parseLogChunkParams(query url.Values) (int64, int64, error) {
	size := int64(logChunkDefaultBytes)
	if raw := query.Get("bytes"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < logChunkMinBytes || value > logChunkMaxBytes {
			return 0, 0, fmt.Errorf("bytes must be a number between %d and %d", logChunkMinBytes, logChunkMaxBytes)
		}
		size = value
	}
	before := int64(-1)
	if raw := query.Get("before"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return 0, 0, fmt.Errorf("before must be a byte offset of 0 or more")
		}
		before = value
	}
	return size, before, nil
}

// logChunkWindow returns the byte range [start, end) of at most size bytes ending at before.
func logChunkWindow(total, size, before int64) (int64, int64) {
	end := total
	if before >= 0 && before < total {
		end = before
	}
	start := end - size
	if start < 0 {
		start = 0
	}
	return start, end
}

// alignLogChunk drops a partial first line. data holds one extra byte before start
// when start > 0, so a window that already begins on a fresh line keeps that line.
// When the window holds no complete line, the partial line is kept so paging always moves back.
func alignLogChunk(data []byte, start int64) ([]byte, int64) {
	if start == 0 {
		return data, 0
	}
	if len(data) == 0 {
		return data, start
	}
	if data[0] == '\n' {
		return data[1:], start
	}
	for i := 1; i < len(data)-1; i++ {
		if data[i] == '\n' {
			return data[i+1:], start + int64(i)
		}
	}
	return data[1:], start
}

func sliceLogChunk(content []byte, size, before int64) ([]byte, int64, int64) {
	total := int64(len(content))
	start, end := logChunkWindow(total, size, before)
	readFrom := start
	if start > 0 {
		readFrom = start - 1
	}
	chunk, offset := alignLogChunk(content[readFrom:end], start)
	return chunk, offset, end
}

// readLogChunk reads one window of a log. Local logs are read with ReadAt so a
// large file is never loaded whole; S3 pointer logs are fetched and then sliced.
func readLogChunk(ctx context.Context, logFile string, size, before int64) (*logChunk, error) {
	file, err := os.Open(logFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	total := info.Size()

	probeSize := total
	if probeSize > logChunkHeaderProbe {
		probeSize = logChunkHeaderProbe
	}
	probe := make([]byte, probeSize)
	if _, err := file.ReadAt(probe, 0); err != nil && err != io.EOF {
		return nil, err
	}
	fields := parseLogKeyValueFields(string(probe))
	chunk := &logChunk{Fields: fields, Storage: "local", Modified: info.ModTime()}

	if _, isPointer, _ := detectS3PointerLog(probe); isPointer {
		content, storage, s3URI, err := resolveStoredLogContent(ctx, logFile)
		if err != nil {
			return nil, err
		}
		chunk.Content, chunk.Offset, chunk.End = sliceLogChunk(content, size, before)
		chunk.Total, chunk.Storage, chunk.S3URI = int64(len(content)), storage, s3URI
		return chunk, nil
	}

	start, end := logChunkWindow(total, size, before)
	readFrom := start
	if start > 0 {
		readFrom = start - 1
	}
	data := make([]byte, end-readFrom)
	if len(data) > 0 {
		if _, err := file.ReadAt(data, readFrom); err != nil && err != io.EOF {
			return nil, err
		}
	}
	chunk.Content, chunk.Offset = alignLogChunk(data, start)
	chunk.End, chunk.Total = end, total
	return chunk, nil
}

// serveLogChunk answers /api/logs/{trace}/content?bytes=N&before=X.
func serveLogChunk(w http.ResponseWriter, r *http.Request, traceID, logFile string) {
	size, before, err := parseLogChunkParams(r.URL.Query())
	if err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	chunk, err := readLogChunk(r.Context(), logFile, size, before)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read log content: "+err.Error())
		return
	}
	data := map[string]interface{}{
		"trace_id":   traceID,
		"content":    string(chunk.Content),
		"offset":     chunk.Offset,
		"end":        chunk.End,
		"total_size": chunk.Total,
		"size":       chunk.Total,
		"filename":   filepath.Base(logFile),
		"cmd_id":     chunk.Fields["cmd_id"],
		"host":       chunk.Fields["host"],
		"job_type":   chunk.Fields["job_type"],
		"modified":   chunk.Modified,
		"storage":    chunk.Storage,
		"s3_uri":     chunk.S3URI,
	}
	sendConfigJSONSuccess(w, http.StatusOK, data, "Log content retrieved successfully")
}
