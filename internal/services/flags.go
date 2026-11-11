package services

import "fmt"

var flagNames = map[byte]string{
	'a': "admin",
	'c': "links",
	'g': "global",
	'h': "hosts",
	'k': "kill",
	's': "services",
}

// needFlag checks that c is an oper with flag f, answering as sv.
func (s *Services) needFlag(c *Client, sv *service, f byte) bool {
	if c.can(f) {
		return true
	}
	s.svcReply(c, sv, fmt.Sprintf("You need oper flag %c (%s) for that.", f, flagNames[f]))
	return false
}
