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
	return nil
}
