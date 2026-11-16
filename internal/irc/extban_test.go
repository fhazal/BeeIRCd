package irc

import "testing"

func TestExtbanSpellings(t *testing.T) {
	for in, want := range map[string]string{
		// UnrealIRCd letters and names
		"~c:#bees":              "~c:#bees",
		"~channel:@#bees":       "~c:@#bees",
		"~O:netadmin*":          "~O:netadmin*",
		"~operclass:admin":      "~O:admin",
		"~S:AB:CD:ef":           "~S:abcdef",
		"~certfp:abcd":          "~S:abcd",
		"~j:*!*@spam":           "~j:*!*@spam",
		"~join:bee":             "~j:bee!*@*",
		"~n:bee":                "~n:bee!*@*",
		"~nickchange:~a:0":      "~n:~a:0",
		"~p:bee":                "~p:bee!*@*",
		"~partmsg:*!*@x":        "~p:*!*@x",
		"~T:block:*badword*":    "~T:block:*badword*",
		"~text:CENSOR:*word*":   "~T:censor:*word*",
		"~m:moderated:~a:Bee":   "~m:moderated:~a:Bee",
		"~msgbypass:external:x": "~m:external:x!*@*",
		"~F:~a:Bee":             "~F:~a:Bee",
		"~flood:bee":            "~F:bee!*@*",
		"~i:#main":              "~i:#main",
		"~inherit:#main":        "~i:#main",
		"~t:10:~j:x":            "~t:10:~j:x!*@*",
		"~f:#jail:~a:x":         "~f:#jail:~a:x",
		"~C:nl":                 "~C:NL",
		"~country:MY":           "~C:MY",
		"~C:N*":                 "~C:N*",
		"~A:64500":              "~A:64500",
		"~asn:AS64500":          "~A:64500",
		"~q:~C:XX":              "~q:~C:XX",
		// InspIRCd names
		"~mute:bee":           "~m:bee!*@*",
		"~nonick:bee":         "~n:bee!*@*",
		"~opertype:Admin":     "~O:Admin",
		"~fingerprint:abcd":   "~S:abcd",
		"~redirect:#jail:bee": "~f:#jail:bee!*@*",
		"~noctcp:bee":         "~noctcp:bee!*@*",
		"~nonotice:~a:x":      "~nonotice:~a:x",
		"~blockcolor:x":       "~blockcolor:x!*@*",
		"~stripcolor:x":       "~stripcolor:x!*@*",
		"~nokick:x":           "~nokick:x!*@*",
		"~blockinvite:x":      "~blockinvite:x!*@*",
		"~unauthed:*!*@*.nl":  "~unauthed:*!*@*.nl",
		"~bot:*":              "~bot:*!*@*",
		"~gateway:beeroom":    "~gateway:beeroom",
		"~oper:Bee*":          "~oper:Bee*",
		"~realmask:*!*@*+bot": "~realmask:*!*@*+bot",
		"~server:leaf.*":      "~server:leaf.*",
		"~q:~server:leaf.*":   "~q:~server:leaf.*",
	} {
		if got, ok := NormalizeBan(in); !ok || got != want {
			t.Errorf("NormalizeBan(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{
		"~c:bees", "~c:@@#bees", "~S:xyz", "~T:hide:x", "~T:block:", "~m:moderated:~q:x",
		"~msgbypass:loud:x", "~i:main", "~q:~j:x", "~F:~T:block:x", "~noctcp:~q:x", "~bot:~a:x",
		"~C:Netherlands", "~C:", "~A:bee", "~A:AS", "~A:12x",
	} {
		if got, ok := NormalizeBan(bad); ok {
			t.Errorf("NormalizeBan(%q) = %q, want refused", bad, got)
		}
	}
}

func TestExtbanLists(t *testing.T) {
	for _, c := range []struct {
		list byte
		mask string
		ok   bool
	}{
		{'b', "~j:x!*@*", true}, {'e', "~j:x!*@*", false},
		{'e', "~m:moderated:x!*@*", true}, {'b', "~m:moderated:x!*@*", false},
		{'b', "~m:x!*@*", true}, {'I', "~m:x!*@*", false},
		{'e', "~F:x!*@*", true}, {'b', "~F:x!*@*", false},
		{'b', "~T:block:*x*", true}, {'I', "~T:block:*x*", false},
		{'I', "~i:#main", true}, {'e', "~i:#main", false},
		{'I', "~a:Bee", true}, {'e', "~t:5:~j:x!*@*", false},
		{'b', "x!*@*", true},
	} {
		if got := ExtbanAllowed(c.list, c.mask); got != c.ok {
			t.Errorf("ExtbanAllowed(%c, %q) = %v", c.list, c.mask, got)
		}
	}
	if ExtbanLetters != "ACFGOSTacfijmnpqrt" {
		t.Errorf("ExtbanLetters = %q", ExtbanLetters)
	}
}

func TestExtbanUnrealAndInsp(t *testing.T) {
	for in, want := range map[string]string{
		"x!*@*":            "x!*@*",
		"~a:Bee":           "~account:Bee",
		"~m:x!*@*":         "~quiet:x!*@*",
		"~q:~a:Bee":        "~quiet:~account:Bee",
		"~t:5:~m:x!*@*":    "~time:5:~quiet:x!*@*",
		"~f:#jail:~a:x":    "~forward:#jail:~account:x",
		"~m:external:~a:x": "~msgbypass:external:~account:x",
		"~T:block:*x*":     "~text:block:*x*",
		"~noctcp:x!*@*":    "~noctcp:x!*@*",
		"~c:@#bees":        "~channel:@#bees",
		"~C:NL":            "~country:NL",
		"~A:64500":         "~asn:64500",
	} {
		if got := UnrealBan(in); got != want {
			t.Errorf("UnrealBan(%q) = %q, want %q", in, got, want)
		}
		// UnrealIRCd's spelling reads back the same
		if back, ok := NormalizeBan(UnrealBan(in)); !ok || (back != in && !(in == "~m:x!*@*" && back == "~q:x!*@*") && !(in == "~t:5:~m:x!*@*" && back == "~t:5:~q:x!*@*")) {
			t.Errorf("NormalizeBan(UnrealBan(%q)) = %q, %v", in, back, ok)
		}
	}
	for in, want := range map[string]string{
		"x!*@*":            "x!*@*",
		"~a:Bee":           "account:Bee",
		"~a:0":             "unauthed:*!*@*",
		"~m:x!*@*":         "mute:x!*@*",
		"~q:~a:Bee":        "mute:account:Bee",
		"~t:5:~n:x!*@*":    "nonick:x!*@*",
		"~f:#jail:x!*@*":   "redirect:#jail:x!*@*",
		"~S:abcd":          "fingerprint:abcd",
		"~noctcp:~c:#bees": "noctcp:channel:#bees",
		"~C:NL":            "country:NL",
	} {
		if got, ok := InspBan(in); !ok || got != want {
			t.Errorf("InspBan(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, unreal := range []string{"~j:x!*@*", "~p:x!*@*", "~T:block:*x*", "~m:notice:x!*@*", "~F:x!*@*", "~i:#main", "~A:64500"} {
		if got, ok := InspBan(unreal); ok {
			t.Errorf("InspBan(%q) = %q, want none", unreal, got)
		}
	}
	for in, want := range map[string]string{
		"*!*@host":        "*!*@host",
		"*!*@2001:db8::1": "*!*@2001:db8::1",
		"R:Bee":           "~a:Bee",
		"account:Bee":     "~a:Bee",
		"m:R:Bee":         "~q:~a:Bee",
		"mute:*!*@x":      "~q:*!*@x",
		"U:*!*@*":         "~unauthed:*!*@*",
		"d:#jail:*!*@x":   "~f:#jail:*!*@x",
		"nonick:j:@#bees": "~n:~c:@#bees",
		"z:abcd":          "~S:abcd",
		"C:*!*@x":         "~noctcp:*!*@x",
	} {
		if got, ok := FromInspBan(in); !ok || got != want {
			t.Errorf("FromInspBan(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"!R:Bee", "X:foo", "share:#chan"} {
		if got, ok := FromInspBan(bad); ok {
			t.Errorf("FromInspBan(%q) = %q, want refused", bad, got)
		}
	}
}

func TestServerExtban(t *testing.T) {
	for in, want := range map[string]string{"~C:nl": "~C:NL", "~country:MY": "~C:MY", "~asn:AS4788": "~A:4788", "~A:64500": "~A:64500"} {
		if got, ok := ServerExtban(in); !ok || got != want {
			t.Errorf("ServerExtban(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"*@host", "~a:Bee", "~q:~C:NL", "~c:#x", "~C:", "~A:x"} {
		if got, ok := ServerExtban(bad); ok {
			t.Errorf("ServerExtban(%q) = %q", bad, got)
		}
	}
	if u, h := SplitServerExtban("~C:NL", true); u != "~country:" || h != "NL" {
		t.Errorf("unreal split %q %q", u, h)
	}
	if u, h := SplitServerExtban("~A:4788", false); u != "~A:" || h != "4788" {
		t.Errorf("bee split %q %q", u, h)
	}
	// ~bot@host is an ident ban, never a place
	if IsPlaceMask("~bot@*.example.net") || !IsPlaceMask("~C:NL") || IsPlaceUser("~bot") || !IsPlaceUser("~country:") {
		t.Error("IsPlaceMask / IsPlaceUser")
	}
}
