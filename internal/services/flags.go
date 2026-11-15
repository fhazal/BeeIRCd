package services

var flagNames = map[byte]string{
	'a': "admin",
	'b': "bots",
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
	s.say(c, sv, "You need oper flag %c (%s) for that.", f, flagNames[f])
	s.logDenied(c, sv) // Anope: a command whose permission the oper lacks
	return false
}
