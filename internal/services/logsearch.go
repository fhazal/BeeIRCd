package services

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// OperServ LOGSEARCH, as in Anope (os_logsearch): search the services log
// file ([services] log_file) and the one before it (<file>.1).

const (
	logSearchDays     = 7   // days back without +<days>d
	logSearchLimit    = 50  // matches shown without +<limit>l
	logSearchMaxLimit = 500 // the most +<limit>l may ask for
	logSearchMaxDays  = 3650
)

// osLogSearch is LOGSEARCH [+<days>d] [+<limit>l] <pattern>.
func osLogSearch(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'a') {
		return
	}
	days, limit := logSearchDays, logSearchLimit
	for len(args) > 0 && len(args[0]) > 2 && args[0][0] == '+' {
		a := args[0]
		n, err := strconv.Atoi(a[1 : len(a)-1])
		if err != nil || n < 1 {
			break // not an option: the pattern starts here
		}
		if unit := a[len(a)-1] | 0x20; unit == 'd' {
			days = min(n, logSearchMaxDays)
		} else if unit == 'l' {
			limit = min(n, logSearchMaxLimit)
		} else {
			break
		}
		args = args[1:]
	}
	pattern := strings.Join(args, " ")
	if pattern == "" {
		s.os(c, "Syntax: LOGSEARCH [+<days>d] [+<limit>l] <pattern>")
		return
	}
	path := s.cfg.Services.LogFile
	if path == "" || s.logw == nil {
		s.os(c, "Services don't log to a file; set [services] log_file first.")
		return
	}
	s.osLog(c, "for %s", pattern)
	// Lines start with the time and service, so the pattern is always
	// wrapped in * (as Anope does): "kill*" finds kills too.
	if !strings.HasPrefix(pattern, "*") {
		pattern = "*" + pattern
	}
	if !strings.HasSuffix(pattern, "*") {
		pattern += "*"
	}
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	go func() {
		found, total := searchLogFiles([]string{path + ".1", path}, pattern, since, limit)
		s.post(func() {
			if c.closed {
				return
			}
			if total == 0 {
				s.os(c, "No matches.")
				return
			}
			for _, line := range found {
				s.os(c, "%s", line)
			}
			s.os(c, "Showing %d of %d matches.", len(found), total)
		})
	}()
}

// searchLogFiles reads files in order (oldest first) and returns the
// newest limit lines from since on that match pattern, oldest first, and
// how many matched in all. A file that isn't there is skipped.
func searchLogFiles(files []string, pattern string, since time.Time, limit int) ([]string, int) {
	ring := make([]string, 0, limit)
	next, total := 0, 0
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if len(line) < len(logTimeFormat) {
				continue
			}
			at, err := time.ParseInLocation(logTimeFormat, line[:len(logTimeFormat)], time.Local)
			if err != nil || at.Before(since) || !irc.Match(pattern, line) {
				continue
			}
			total++
			if len(ring) < limit {
				ring = append(ring, line)
			} else {
				ring[next] = line
				next = (next + 1) % limit
			}
		}
		f.Close()
	}
	return append(ring[next:], ring[:next]...), total
}
