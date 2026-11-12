package services

// Stage 2e: K-lines, bans for this server only (/KLINE, /UNKLINE). They
// live in the network ban table as kind "kline" but never go over links.

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// AddKline adds or replaces a K-line; life 0 = until removed.
func (s *Services) AddKline(mask, reason, setBy string, life time.Duration) error {
	b := store.NetBan{Kind: store.BanKline, Mask: strings.ToLower(mask), Reason: reason, SetBy: setBy, SetAt: time.Now()}
	if life > 0 {
		b.Expires = b.SetAt.Add(life)
	}
	if err := s.store.AddNetBan(b); err != nil {
		return err
	}
	s.reloadOperData()
	return nil
}

// DelKline removes a K-line; false when there was none.
func (s *Services) DelKline(mask string) (bool, error) {
	gone, err := s.store.DelNetBan(store.BanKline, strings.ToLower(mask))
	if gone {
		s.reloadOperData()
	}
	return gone, err
}

// Klines are the K-lines in force.
func (s *Services) Klines() []store.NetBan { return s.liveBans(store.BanKline) }
