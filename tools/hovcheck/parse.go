package main

import (
	"bufio"
	"net"
	"strings"
)

// msg is one IRC line taken apart.
type msg struct {
	tags, prefix, cmd string
	params            []string
}

func parse(line string) msg {
	var m msg
	if strings.HasPrefix(line, "@") {
		m.tags, line, _ = strings.Cut(line[1:], " ")
	}
	if strings.HasPrefix(line, ":") {
		m.prefix, line, _ = strings.Cut(line[1:], " ")
	}
	for line != "" {
		if strings.HasPrefix(line, ":") {
			m.params = append(m.params, line[1:])
			break
		}
		var p string
		p, line, _ = strings.Cut(line, " ")
		if p == "" {
			continue
		}
		if m.cmd == "" {
			m.cmd = strings.ToUpper(p)
		} else {
			m.params = append(m.params, p)
		}
	}
	return m
}

func (m msg) param(i int) string {
	if i < len(m.params) {
		return m.params[i]
	}
	return ""
}

func (m msg) last() string {
	if len(m.params) == 0 {
		return ""
	}
	return m.params[len(m.params)-1]
}

func nickOf(prefix string) string {
	n, _, _ := strings.Cut(prefix, "!")
	return n
}

type lineReader struct{ r *bufio.Reader }

func newLineReader(c net.Conn) *lineReader { return &lineReader{bufio.NewReaderSize(c, 64<<10)} }

func (l *lineReader) next() (string, error) {
	s, err := l.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}
