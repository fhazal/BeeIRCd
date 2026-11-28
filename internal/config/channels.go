package config

import (
	"fmt"
	"strings"
)

// Channels is the [channels] section.
type Channels struct {
	// FloodProfile is the +F profile every channel without its own +F
	// uses, like UnrealIRCd's anti-flood::channel::default-profile.
	// "off" switches the default off. Default "normal".
	FloodProfile string `toml:"flood_profile"`
	// Create is who may make a new channel: "everyone" (the default),
	// "registered" (logged in to NickServ) or "opers".
	Create string `toml:"create"`
	// HideBans lets only ops (and opers) read a channel's ban list. The
	// exception and invite lists are for ops only either way.
	HideBans bool `toml:"hide_bans"`
	// HideLists are the lists only ops (and opers) may read, like
	// InspIRCd's hidelist: "b" bans, "e" exceptions, "I" invite
	// exceptions. hide_bans = true counts as "b".
	HideLists string `toml:"hide_lists"`
	// HideModes are channel mode letters whose changes only ops (@ and
	// up) and whoever made the change are shown, like InspIRCd's
	// hidemode. Others get the rest of the line, or nothing.
	HideModes string `toml:"hide_modes"`
}

// ChannelModeLetters are every channel mode BeeIRCd knows, ranks
// included (hide_modes may only name these).
const ChannelModeLetters = "beIkBEFHJLWflCDGKMNOPQRSTUVZcimnprstuzqaohv"

// HidesList reports whether only ops (and opers) may read list l.
func (c *Channels) HidesList(l byte) bool {
	return l == 'b' && c.HideBans || strings.IndexByte(c.HideLists, l) >= 0
}

// FloodProfiles are the +F profiles, as UnrealIRCd 6.2 ships them. The p
// (paste) part is left out: BeeIRCd has no paste flood check.
var FloodProfiles = map[string]string{
	"very-strict":  "[10j#R10,30m#M10,7c#C15,5n#N15,10k#K15]:15",
	"strict":       "[15j#R10,40m#M10,7c#C15,8n#N15,10k#K15]:15",
	"normal":       "[30j#R10,40m#M10,7c#C15,8n#N15,10k#K15]:15",
	"relaxed":      "[45j#R10,60m#M10,7c#C15,10n#N15,10k#K15]:15",
	"very-relaxed": "[60j#R10,90m#M10,7c#C15,10n#N15,10k#K15]:15",
	"off":          "",
}

// ProfileNames lists the +F profiles from strictest to loosest.
func ProfileNames() []string {
	return []string{"very-strict", "strict", "normal", "relaxed", "very-relaxed", "off"}
}

func (c *Channels) setDefaults() {
	if c.FloodProfile == "" {
		c.FloodProfile = "normal"
	}
	c.FloodProfile = strings.ToLower(c.FloodProfile)
}

func (c *Channels) check() error {
	if _, ok := FloodProfiles[c.FloodProfile]; !ok {
		return fmt.Errorf("[channels] flood_profile %q: use one of %s", c.FloodProfile, strings.Join(ProfileNames(), ", "))
	}
	for _, l := range c.HideLists {
		if !strings.ContainsRune("beI", l) {
			return fmt.Errorf("[channels] hide_lists %q: use the letters b (bans), e (exceptions) and I (invite exceptions)", c.HideLists)
		}
	}
	for _, l := range c.HideModes {
		if !strings.ContainsRune(ChannelModeLetters, l) {
			return fmt.Errorf("[channels] hide_modes %q: %c is not a channel mode (HELP CHMODES lists them)", c.HideModes, l)
		}
	}
	return nil
}
