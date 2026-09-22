package gpu

// tailWriter is a bounded writer: String returns at most the last max bytes written to it.
//
// The buffer grows to twice max before compacting, so compaction costs O(1) per byte written.
type tailWriter struct {
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n >= w.max {
		w.buf = append(w.buf[:0], p[n-w.max:]...)
		return n, nil
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) > 2*w.max {
		w.buf = w.buf[:copy(w.buf, w.buf[len(w.buf)-w.max:])]
	}
	return n, nil
}

func (w *tailWriter) String() string {
	// Converting to string copies; returning a slice of buf would keep the whole buffer alive.
	if len(w.buf) > w.max {
		return string(w.buf[len(w.buf)-w.max:])
	}
	return string(w.buf)
}
