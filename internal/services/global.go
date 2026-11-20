package services

// OperServ RELOAD, as in Anope 2.1 (os_reload).

// osReload is OperServ RELOAD: read beeircd.conf again, like /REHASH.
func osReload(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'a') {
		return
	}
	if err := s.host.Rehash(c.user.Nick); err != nil {
		s.os(c, "%v", err)
		return
	}
	s.audit(c, s.operServ, "RELOAD")
	s.osLog(c, "")
	s.os(c, "Reloaded the config.")
}
