//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// onHangup calls f for every SIGHUP until the returned stop is called.
func onHangup(f func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				f()
			case <-done:
				return
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
