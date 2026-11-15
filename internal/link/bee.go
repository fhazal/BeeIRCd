package link

import "strings"

// Bee is a line only BeeIRCd servers send each other: ":<sid> BEE <sub>
// <args...>". Other server software never gets one. Sub names what it is
// (MARKREAD, REDACT); a server that doesn't know a Sub passes it on.
type Bee struct {
	Source string
	Sub    string
	Args   []string
}

func (c *unrealCodec) encodeBee(b *Bee) []string {
	if !c.bee || b.Sub == "" {
		return nil
	}
	line := ":" + b.Source + " BEE " + b.Sub
	for i, a := range b.Args {
		if i == len(b.Args)-1 && (a == "" || strings.ContainsAny(a, " :") || a[0] == ':') {
			line += " :" + a
			break
		}
		line += " " + a
	}
	return []string{line}
}

func decodeBee(src string, p []string) any {
	if len(p) < 1 || p[0] == "" {
		return nil
	}
	return &Bee{Source: src, Sub: strings.ToUpper(p[0]), Args: append([]string(nil), p[1:]...)}
}
