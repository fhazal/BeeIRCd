package server

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

// Lines longer than the 1 KB read buffer are put together; lines over
// maxInput are refused and the next line still reads fine.
func TestReadLineSizes(t *testing.T) {
	mid := "@+tag=" + strings.Repeat("a", 3000) + " PRIVMSG #x :hi"
	huge := strings.Repeat("b", maxInput+100)
	in := "PING :one\r\n" + mid + "\r\n" + huge + "\r\nPING :after\r\n"
	r := bufio.NewReaderSize(strings.NewReader(in), readBufSize)

	if l, err := readLine(r); err != nil || l != "PING :one" {
		t.Fatalf("short line: %q %v", l, err)
	}
	if l, err := readLine(r); err != nil || l != mid {
		t.Fatalf("3 KB line: len %d %v", len(l), err)
	}
	if _, err := readLine(r); !errors.Is(err, errTooLong) {
		t.Fatalf("over the limit: %v", err)
	}
	if l, err := readLine(r); err != nil || l != "PING :after" {
		t.Fatalf("line after a long one: %q %v", l, err)
	}
	exact := strings.Repeat("c", maxInput-2)
	r = bufio.NewReaderSize(strings.NewReader(exact+"\r\n"), readBufSize)
	if l, err := readLine(r); err != nil || l != exact {
		t.Fatalf("line at the limit: len %d %v", len(l), err)
	}
}
