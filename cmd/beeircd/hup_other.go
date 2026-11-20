//go:build !unix

package main

// ignoreHandoverSignal: no gate here, so no hand-over signal.
func ignoreHandoverSignal() {}

// onHangup: there is no SIGHUP here; beeircd rehash still works.
func onHangup(func()) (stop func()) { return func() {} }
