//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// ignoreHandoverSignal makes the server process behind the gate deaf to
// SIGUSR2. That signal asks the gate for a hand-over; sent to the whole
// service (systemctl kill) it reaches this process too, and would end it.
func ignoreHandoverSignal() { signal.Ignore(syscall.SIGUSR2) }

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
