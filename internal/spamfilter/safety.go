package spamfilter

// The safety net: before a new rule goes on, it is tried on normal chat.
// A rule that would catch any of these lines is refused unless the oper
// says "add anyway" (Hive Panel) or -force (/SPAMFILTER).

// normalText are everyday lines from IRC channels, English and Malay.
var normalText = []string{
	"hello", "hi all", "hey everyone", "good morning", "good night", "morning all", "evening folks",
	"hello everyone, how are you?", "how are you today?", "i'm fine thanks", "not bad, you?", "thanks!", "thank you so much",
	"thx", "ty", "np", "no problem", "you're welcome", "welcome back", "wb", "brb", "bbl", "afk for a bit", "back",
	"lol", "lmao", "haha", "hahaha", "rofl", ":)", ":D", ";)", ":(", ":P", "<3", "xD", "^_^", "o/", "\\o/",
	"ok", "okay", "k", "sure", "yes", "no", "maybe", "idk", "i don't know", "i think so", "i agree", "same here",
	"what do you mean?", "can you explain?", "anyone here?", "is anyone around?", "who is online?", "where is everyone?",
	"free tonight?", "feel free to ask", "i'm free after 8", "the download is free", "it's free and open source",
	"check out this link", "here is the link: https://example.org", "see https://github.com/fhazal/BeeIRCd",
	"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "look at https://en.wikipedia.org/wiki/IRC", "join #help if you need help",
	"come to #lobby", "/join #beeircd", "type /msg NickServ help", "use /ns identify", "register your nick with nickserv",
	"what's the server address?", "the port is 6697 with tls", "my client keeps disconnecting", "ping timeout again",
	"anyone using hexchat?", "i use weechat", "irssi is great", "the web chat works fine", "my internet is slow today",
	"i need help with my bot", "how do i set a vhost?", "how to get voice?", "can i have op?", "who is the channel founder?",
	"the topic is outdated", "please update the topic", "nice topic", "great server", "this network is awesome",
	"i love this channel", "nice to meet you", "see you tomorrow", "see you later", "bye", "bye all", "cya", "gn", "take care",
	"happy birthday!", "congrats!", "well done", "good job", "that's cool", "awesome", "nice one", "interesting",
	"what time is it there?", "it's raining here", "so hot today", "i'm hungry", "time for lunch", "coffee time",
	"just got home", "going to sleep", "working late again", "i have an exam tomorrow", "weekend finally", "monday again",
	"did you watch the match?", "great game last night", "who won?", "what are you playing?", "anyone up for a game?",
	"my code doesn't compile", "try restarting it", "did you check the logs?", "works on my machine", "update your client",
	"the new version is out", "release notes are on the site", "i found a bug", "please report it on github", "thanks for the fix",
	"selamat pagi", "selamat petang", "selamat malam", "apa khabar?", "khabar baik", "terima kasih", "sama-sama",
	"jom borak", "jom makan", "dah makan?", "belum lagi", "saya dari kl", "kat mana?", "ok boleh", "tak apa",
	"nanti saya cuba", "betul ke?", "tak tahu lah", "mana semua orang?", "selamat datang", "salam semua", "waalaikumsalam",
	"hari ni hujan", "panas betul hari ni", "esok kerja", "cuti sekolah dah mula", "selamat hari raya", "maaf zahir batin",
	"boleh tolong saya?", "macam mana nak daftar nick?", "nak join channel apa?", "server ni laju", "best channel ni",
	"saya baru sini", "kenalkan diri", "umur berapa?", "kerja apa?", "belajar lagi", "jumpa lagi", "bye semua",
	"nak tanya sikit", "ada sesiapa?", "saya pun sama", "betul tu", "setuju", "haha lawak", "kelakar betul",
	"I'm looking for a job", "sell me your old laptop?", "buy me a coffee sometime", "free shipping is nice",
	"discount codes never work for me", "my followers on github grew", "crypto is too risky for me",
	"the doctor said rest", "my cat is sleeping", "dinner is ready", "music recommendations?", "listening to the radio",
	"radio stream is down", "the stream is back up", "who is the dj tonight?", "play some jazz please", "nice song",
	"ACTION waves", "ACTION is away", "^ that", "+1", "-1", "same", "true", "fair enough", "good point", "exactly",
	"wait what", "really?", "no way", "omg", "wow", "hmm", "uh oh", "oops", "my bad", "sorry", "sorry for the spam",
}

// normalUsers are nick!user@host:realname lines for the 'u' target.
var normalUsers = []string{
	"Belon!~belon@cloak-1a2b3c.example.org:Belon", "fhazal!fhazal@bajubesi.org:BajuBesi Labs", "guest42!~guest@127.0.0.1:BeeRoom user",
	"Ahmad!~ahmad@202.188.1.1:Ahmad bin Ali", "sarah_!sarah@host.example.net:Sarah", "Nurul!~nurul@cloak-ffee.my:Nurul Aina",
	"john|away!~john@10.0.0.5:John Smith", "[Bot]Helper!bot@services.example:Helper bot", "Mike`!mike@d1234.isp.example.com:mike",
	"Siti!~siti@2001:db8::1:Siti", "kopi!~kopi@cloak-9z.bee:Kopi O", "Zul!zul@tm.net.my:Zulkifli",
}

// SafetyHits returns up to max normal lines that f would catch.
func SafetyHits(f Filter, max int) []string {
	r, err := Compile(f)
	if err != nil {
		return nil
	}
	var out []string
	try := func(lines []string) {
		for _, l := range lines {
			if len(out) >= max {
				return
			}
			if r.Catches(Clean(l)) {
				out = append(out, l)
			}
		}
	}
	if f.Targets != "u" {
		try(normalText)
	}
	if f.Has(User) {
		try(normalUsers)
	}
	return out
}

// SafetyCount is how many normal lines there are (for "tried on N lines").
func SafetyCount(f Filter) int {
	n := 0
	if f.Targets != "u" {
		n += len(normalText)
	}
	if f.Has(User) {
		n += len(normalUsers)
	}
	return n
}
