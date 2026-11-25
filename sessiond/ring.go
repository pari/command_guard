package main

// ringBuffer keeps the most recent `size` bytes written to it. It is used to
// replay recent terminal output when a client re-attaches to a session.
type ringBuffer struct {
	buf   []byte
	start int // index of the oldest byte
	n     int // number of valid bytes
}

func newRingBuffer(size int) *ringBuffer {
	if size < 1024 {
		size = 1024
	}
	return &ringBuffer{buf: make([]byte, size)}
}

func (r *ringBuffer) Write(p []byte) {
	size := len(r.buf)
	if len(p) >= size {
		copy(r.buf, p[len(p)-size:])
		r.start, r.n = 0, size
		return
	}
	end := (r.start + r.n) % size
	first := copy(r.buf[end:], p)
	if first < len(p) {
		copy(r.buf, p[first:])
	}
	if r.n+len(p) > size {
		overflow := r.n + len(p) - size
		r.start = (r.start + overflow) % size
		r.n = size
	} else {
		r.n += len(p)
	}
}

// Bytes returns the buffered output in chronological order.
func (r *ringBuffer) Bytes() []byte {
	out := make([]byte, r.n)
	first := copy(out, r.buf[r.start:min(r.start+r.n, len(r.buf))])
	if first < r.n {
		copy(out[first:], r.buf[:r.n-first])
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
