package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/store"
)

// Hive Chat h2: the Chat tab of the Hive Panel shows BeeRoom ([web]
// chat_url), signed in as the person. The panel asks for a one-time key;
// BeeRoom sends it as the SASL PLAIN password. A key works once, for its
// own account, within chatKeyLife. Only the server loop touches the keys.

const (
	chatKeyLife   = time.Minute
	chatKeyPrefix = "hive-"
	chatKeysMax   = 1000 // keys waiting at once; the oldest go first
)

type chatKey struct {
	account int64
	until   time.Time
}

// newChatKey makes a one-time key for account a.
func (s *Server) newChatKey(a *store.Account, now time.Time) string {
	if s.chatKeys == nil {
		s.chatKeys = map[string]chatKey{}
	}
	for k, v := range s.chatKeys {
		if now.After(v.until) {
			delete(s.chatKeys, k)
		}
	}
	for len(s.chatKeys) >= chatKeysMax {
		var oldest string
		var at time.Time
		for k, v := range s.chatKeys {
			if oldest == "" || v.until.Before(at) {
				oldest, at = k, v.until
			}
		}
		delete(s.chatKeys, oldest)
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	key := chatKeyPrefix + hex.EncodeToString(raw[:])
	s.chatKeys[key] = chatKey{account: a.ID, until: now.Add(chatKeyLife)}
	return key
}

// useChatKey reports whether pw is a live one-time key for account a, and
// spends it. A key that doesn't fit a is spent too: it was shown to the
// wrong account, so it is never tried again.
func (s *Server) useChatKey(a *store.Account, pw string, now time.Time) bool {
	if !strings.HasPrefix(pw, chatKeyPrefix) || s.chatKeys == nil {
		return false
	}
	var found string
	for k := range s.chatKeys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(pw)) == 1 {
			found = k
		}
	}
	if found == "" {
		return false
	}
	ck := s.chatKeys[found]
	delete(s.chatKeys, found)
	return ck.account == a.ID && !now.After(ck.until)
}

// panelChatKey is the Chat tab's sign-in for po (members and opers).
func (s *Server) panelChatKey(po *control.PanelOper) *control.Response {
	if s.cfg.Web.ChatURL == "" {
		return &control.Response{Error: "The Chat tab is off ([web] chat_url is empty)."}
	}
	a, err := s.store.AccountByNick(po.Account)
	if err != nil || a == nil {
		return &control.Response{Error: "Your account no longer exists.", Denied: "signin"}
	}
	key := s.newChatKey(a, time.Now())
	if key == "" {
		return &control.Response{Error: "Could not make a sign-in key. Please try again."}
	}
	return &control.Response{Chat: &control.ChatKey{URL: s.cfg.Web.ChatURL, Account: a.Name, Key: key}}
}
