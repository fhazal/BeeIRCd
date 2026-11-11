package server

import (
	"fmt"
	"time"
)

// loopStats times every piece of work the loop runs (one command, one
// timer tick, one finished password check). Everything waits while the
// loop is busy, so these numbers are the server's lag. OperServ STATS shows
// them.
type loopStats struct {
	count   uint64
	total   time.Duration
	slowest time.Duration
	buckets [16]uint64 // one per edge in loopBuckets, plus "slower"
}

// loopBuckets are the upper edges of the histogram, for percentiles.
var loopBuckets = []time.Duration{
	10 * time.Microsecond, 25 * time.Microsecond, 50 * time.Microsecond, 100 * time.Microsecond,
	250 * time.Microsecond, 500 * time.Microsecond, time.Millisecond, 2 * time.Millisecond,
	5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond,
	100 * time.Millisecond, 250 * time.Millisecond, time.Second,
}

func (l *loopStats) add(d time.Duration) {
	l.count++
	l.total += d
	if d > l.slowest {
		l.slowest = d
	}
	i := 0
	for i < len(loopBuckets) && d > loopBuckets[i] {
		i++
	}
	l.buckets[i]++
}

// percentile returns the bucket edge under which share (0 to 1) of all
// work finished; the last bucket reports the slowest seen.
func (l *loopStats) percentile(share float64) time.Duration {
	if l.count == 0 {
		return 0
	}
	need := uint64(float64(l.count)*share + 0.5)
	var seen uint64
	for i, n := range l.buckets {
		seen += n
		if seen >= need {
			if i < len(loopBuckets) {
				return loopBuckets[i]
			}
			break
		}
	}
	return l.slowest
}

func (l *loopStats) average() time.Duration {
	if l.count == 0 {
		return 0
	}
	return l.total / time.Duration(l.count)
}

// summary is one line for STATS.
func (l *loopStats) summary() string {
	return fmt.Sprintf("avg %s, 99%% under %s, slowest %s (%d runs)",
		ms(l.average()), ms(l.percentile(0.99)), ms(l.slowest), l.count)
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.2f ms", float64(d)/float64(time.Millisecond))
}
