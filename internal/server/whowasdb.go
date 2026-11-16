package server

import (
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// WHOWAS survives a restart: the entries are saved to the database every
// few minutes and when the server stops, and read back at start
// (UnrealIRCd's whowasdb). Entries older than a week are not kept.

const (
	whowasSaveEvery = 15 * time.Minute
	whowasKeep      = 7 * 24 * time.Hour
)

// loadWhowas reads the saved entries.
func (s *Server) loadWhowas(now time.Time) {
	s.whowasSaved = now
	all, err := s.store.Whowas()
	if err != nil {
		s.log.Printf("services database: whowas: %v", err)
		return
	}
	for _, e := range all {
		if now.Sub(e.At) <= whowasKeep {
			s.whowas = append(s.whowas, whowasEntry{e.Nick, e.Ident, e.Host, e.Realname, e.Server, e.At})
		}
	}
	if len(s.whowas) > whowasSize {
		s.whowas = s.whowas[len(s.whowas)-whowasSize:]
	}
}

func (s *Server) whowasTick(now time.Time) {
	if s.whowasDirty && now.Sub(s.whowasSaved) >= whowasSaveEvery {
		s.saveWhowas(now)
	}
}

// saveWhowas writes the entries when something changed.
func (s *Server) saveWhowas(now time.Time) {
	s.whowasSaved = now
	if !s.whowasDirty {
		return
	}
	all := make([]store.Whowas, 0, len(s.whowas))
	for _, e := range s.whowas {
		if now.Sub(e.at) <= whowasKeep {
			all = append(all, store.Whowas{Nick: e.nick, Ident: e.ident, Host: e.host, Realname: e.realname, Server: e.server, At: e.at})
		}
	}
	if err := s.store.SaveWhowas(all); err != nil {
		s.log.Printf("services database: whowas: %v", err)
		return
	}
	s.whowasDirty = false
}
