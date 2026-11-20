package services

import (
	"os"
	"sync/atomic"
	"time"
)

// The services log file ([services] log_file): every line the bots post to
// the log channel, also written to disk as
//
//	2026-11-20 01:31:05 OperServ: <text>
//
// (local time, the service's nick, colours and bold taken out). A goroutine
// of its own does the writing, so the loop never waits for the disk: lines
// queue in a buffer, and when it is full they are dropped and counted.

// logFileBuffer is how many lines may wait for the writer.
const logFileBuffer = 1024

// logTimeFormat starts every line of the file.
const logTimeFormat = "2006-01-02 15:04:05"

// logWriter appends lines to one log file from its own goroutine.
type logWriter struct {
	path   string
	max    int64 // rotate once the file is this big
	lines  chan string
	done   chan struct{}
	failed atomic.Bool  // opening or writing failed: lines are dropped until a rehash
	drops  atomic.Int64 // lines dropped because the buffer was full
	report func(error)  // told once, from the writer goroutine, on the first error
}

// startLogWriter opens path (in the writer goroutine) and starts writing.
func startLogWriter(path string, max int64, report func(error)) *logWriter {
	w := &logWriter{path: path, max: max, lines: make(chan string, logFileBuffer), done: make(chan struct{}), report: report}
	go w.run()
	return w
}

// add queues line without waiting: a full buffer drops it.
func (w *logWriter) add(line string) {
	select {
	case w.lines <- line:
	default:
		w.drops.Add(1)
	}
}

// close stops the writer once the queued lines are written. It doesn't
// wait; wait does.
func (w *logWriter) close() { close(w.lines) }

// wait waits up to d for the writer to finish after close.
func (w *logWriter) wait(d time.Duration) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (w *logWriter) fail(err error) {
	if w.failed.CompareAndSwap(false, true) && w.report != nil {
		w.report(err)
	}
}

func (w *logWriter) open() (*os.File, int64, error) {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (w *logWriter) run() {
	defer close(w.done)
	f, size, err := w.open()
	if err != nil {
		w.fail(err)
	}
	for line := range w.lines {
		if f == nil {
			continue // failed: drop until a rehash starts a new writer
		}
		n, err := f.WriteString(line + "\n")
		size += int64(n)
		if err != nil {
			w.fail(err)
			f.Close()
			f = nil
			continue
		}
		if size >= w.max {
			f.Close()
			f = nil
			if err := os.Rename(w.path, w.path+".1"); err != nil {
				w.fail(err)
				continue
			}
			if f, size, err = w.open(); err != nil {
				f = nil
				w.fail(err)
			}
		}
	}
	if f != nil {
		f.Close()
	}
}

// logFileOn reports whether services write a log file.
func (s *Services) logFileOn() bool { return s.logw != nil }

// logFileLine queues text from sv for the log file.
func (s *Services) logFileLine(sv *service, text string) {
	if s.logw == nil || sv == nil {
		return
	}
	s.logw.add(s.now().Format(logTimeFormat) + " " + sv.user.Nick + ": " + normalizeBuffer(text))
}

// SyncLogFile starts, changes or stops the log file writer after the config
// changed (start, rehash): a new path, a new size cap or an earlier error
// starts a fresh writer; an empty path stops it.
func (s *Services) SyncLogFile() {
	path, max := s.cfg.Services.LogFile, int64(s.cfg.Services.LogFileMaxMB)<<20
	if s.off {
		path = ""
	}
	if max <= 0 {
		max = 10 << 20
	}
	if w := s.logw; w != nil {
		if w.path == path && w.max == max && !w.failed.Load() {
			return
		}
		w.close() // it finishes the lines it has on its own
		s.logw = nil
	}
	if path == "" {
		return
	}
	s.logw = startLogWriter(path, max, func(err error) {
		s.post(func() {
			s.snotice('a', "Services can't write the log file %s: %v (trying again at the next rehash)", path, err)
		})
	})
}

// CloseLogFile writes the lines still queued and closes the log file (at
// shutdown).
func (s *Services) CloseLogFile() {
	w := s.logw
	if w == nil {
		return
	}
	s.logw = nil
	w.close()
	if !w.wait(3 * time.Second) {
		s.log.Printf("the services log file %s did not close in time", w.path)
	}
	if n := w.drops.Load(); n > 0 {
		s.log.Printf("the services log file %s dropped %s (the writer fell behind)", w.path, plural(int(n), "line"))
	}
}
