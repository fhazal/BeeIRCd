//go:build !unix

package main

// onHangup: there is no SIGHUP here; beeircd rehash still works.
func onHangup(func()) (stop func()) { return func() {} }
