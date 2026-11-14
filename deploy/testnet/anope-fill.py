"""Fills a sandbox Anope 2.0 with one of everything its database can hold,
through a BeeIRCd hub (built-in services off), for the Anope import tests.

Usage: anope-fill.py host port [full|nicks]
Prints every services reply so a failed command is easy to spot."""
import re, socket, sys, time

HOST, PORT = sys.argv[1], int(sys.argv[2])
MODE = sys.argv[3] if len(sys.argv) > 3 else "full"
PW = {"zfill": "fill-pass-zfill", "alice": "fill-pass-alice", "bob": "fill-pass-bob",
      "carol": "fill-pass-carol", "dave": "fill-pass-dave", "eve": "fill-pass-eve"}


class C:
    def __init__(self, nick):
        self.s = socket.create_connection((HOST, PORT), timeout=0.5)
        self.buf, self.nick = b"", nick
        self.send("NICK " + nick)
        self.send("USER " + nick + " 0 * :anope fill " + nick)
        self.wait(" 001 ", 10)

    def send(self, line):
        self.s.sendall((line + "\r\n").encode())

    def read(self, secs):
        out, end = [], time.time() + secs
        while time.time() < end:
            try:
                d = self.s.recv(65536)
            except socket.timeout:
                continue
            if not d:
                break
            self.buf += d
            while b"\r\n" in self.buf:
                l, self.buf = self.buf.split(b"\r\n", 1)
                l = l.decode("utf-8", "replace")
                if l.startswith("PING"):
                    self.send("PONG" + l[4:])
                out.append(l)
        return out

    def wait(self, pat, secs=5):
        end = time.time() + secs
        while time.time() < end:
            for l in self.read(0.5):
                if re.search(pat, l):
                    return l
        raise SystemExit("FAIL %s: no %r" % (self.nick, pat))

    def say(self, svc, text, secs=4):
        """Sends text to svc and prints its replies: waits for the first
        one (at most secs), then for a short quiet moment."""
        self.send("PRIVMSG %s :%s" % (svc, text))
        shown = text.split(" ")[0] if text.upper().startswith(("REGISTER", "GROUP", "IDENTIFY")) else text
        got, end = False, time.time() + secs
        while time.time() < end:
            lines = self.read(0.6 if got else 0.3)
            notes = [l for l in lines if " NOTICE " + self.nick + " :" in l]
            for l in notes:
                print("  [%s] %s -> %s" % (self.nick, shown, l.split(" :", 1)[1]))
            if got and not notes:
                return
            got = got or bool(notes)
        if not got:
            print("  [%s] %s -> (no reply)" % (self.nick, shown))


def user(nick, email=None):
    c = C(nick)
    c.say("NickServ", "REGISTER %s %s" % (PW[nick], email or nick + "@example.org"))
    return c


z = user("zfill")
if MODE != "full":
    a = user("alice")
    z.say("OperServ", "UPDATE", 3)
    sys.exit(0)

a = user("alice")
b = user("bob")
c = user("carol")
d = user("dave")
e = user("eve")

# NickServ settings, access, certificate, auto-join, greet, grouping.
for t in ["SET KILL QUICK", "SET SECURE ON", "SET PRIVATE ON", "SET HIDE EMAIL ON", "SET HIDE USERMASK ON",
          "SET HIDE QUIT ON", "SET AUTOOP OFF", "SET GREET hello from alice", "SET KEEPMODES ON",
          "ACCESS ADD alice@10.0.0.*", "CERT ADD 0f2e3d4c5b6a79881726354453627180a1b2c3d4e5f60718293a4b5c6d7e8f90",
          "AJOIN ADD #bee", "AJOIN ADD #keyed sekrit", "SET URL https://alice.example.org"]:
    a.say("NickServ", t)
a2 = C("alice2")
a2.say("NickServ", "GROUP alice " + PW["alice"])
b.say("MemoServ", "SET LIMIT 7")
b.say("MemoServ", "SET NOTIFY LOGON")
b.say("MemoServ", "IGNORE ADD carol")
z.say("NickServ", "SASET NOEXPIRE dave ON")
z.say("NickServ", "SUSPEND carol +7d testing the import")
z.say("NickServ", "SASET DISPLAY alice alice")

# Channels: register, settings, access in all three styles, akick, levels, mode lock, entry message.
for ch, who in (("#bee", a), ("#hive", b), ("#quiet", d)):
    who.send("JOIN " + ch)
    who.read(1)
    who.say("ChanServ", "REGISTER %s the %s channel" % (ch, ch[1:]))
a.send("TOPIC #bee :Welcome to the hive")
a.read(1)
for t in ["SET SUCCESSOR #bee bob", "SET KEEPTOPIC #bee ON", "SET PEACE #bee ON", "SET SECUREOPS #bee ON",
          "SET SIGNKICK #bee ON", "SET PERSIST #bee ON", "SET PRIVATE #bee ON", "SET KEEPMODES #bee ON",
          "SET BANTYPE #bee 3", "SET URL #bee https://bee.example.org", "SET EMAIL #bee bee@example.org",
          "TOPIC #bee LOCK ON", "MODE #bee LOCK ADD +ntk honey", "MODE #bee LOCK ADD -s", "MODE #bee LOCK ADD +l 50",
          "ACCESS #bee ADD bob 5", "ACCESS #bee ADD carol 10", "ACCESS #bee ADD *!*@trusted.host 3",
          "AOP #bee ADD dave", "SOP #bee ADD zfill", "VOP #bee ADD alice2",
          "FLAGS #bee MODIFY eve +AFOfiortv",
          "AKICK #bee ADD *!*@bad.host spammer", "AKICK #bee ADD eve2 not welcome",
          "LEVELS #bee SET TOPIC 4", "LEVELS #bee DISABLE GETKEY",
          "ENTRYMSG #bee ADD Be kind, stay curious", "ENTRYMSG #bee ADD Second line",
          "LOG #bee ChanServ/ACCESS MESSAGE"]:
    a.say("ChanServ", t)
z.say("ChanServ", "SUSPEND #hive +7d testing the import")

# BotServ: a bot, assigned, with greet, fantasy, kickers and bad words.
for t in ["BOT ADD Honey honey bee.example.org Honey the bot", "BOT ADD Drone drone drone.example.org Drone"]:
    z.say("BotServ", t)
for t in ["ASSIGN #bee Honey", "SET GREET #bee ON", "SET FANTASY #bee ON", "SET DONTKICKOPS #bee ON",
          "KICK CAPS #bee ON 3 10 25", "KICK BADWORDS #bee ON 2", "KICK FLOOD #bee ON 0 6 10",
          "BADWORDS #bee ADD spam", "BADWORDS #bee ADD casino", "SET BANEXPIRE #bee 60"]:
    a.say("BotServ", t)
d.say("BotServ", "ASSIGN #quiet Drone")

# HostServ: a vhost set by an oper, and a pending request.
z.say("HostServ", "SET alice cool.alice.vhost")
z.say("HostServ", "SET dave dave@ident.vhost")
b.say("HostServ", "REQUEST bob.wants.vhost")

# MemoServ: a nick memo, a channel memo.
time.sleep(3)
a.say("MemoServ", "SEND bob Hello bob, this memo should survive the move")
time.sleep(3)
a.say("MemoServ", "SEND #bee Channel memo for #bee")

# OperServ: network bans, forbids, sessions, news, info, ignore.
for t in ["AKILL ADD +7d *@203.0.113.9 test akill", "AKILL ADD +0 *@198.51.100.0/24 forever",
          "SQLINE ADD +7d BadNick* reserved", "SNLINE ADD +7d *bot*:bots are not welcome",
          "FORBID ADD NICK +30d evil* no evil nicks", "FORBID ADD CHAN #warez* no warez",
          "FORBID ADD EMAIL *@spam.example no spam mail", "FORBID ADD REGISTER #nope* no",
          "EXCEPTION ADD +30d 192.0.2.10 10 the office", "EXCEPTION ADD 192.0.2.11 0 unlimited",
          "NEWS ADD logon hello to everyone", "OPERNEWS ADD for opers only", "RANDOMNEWS ADD a random tip",
          "INFO ADD alice good person", "INFO ADD #bee main channel",
          "IGNORE ADD +1h *!*@ignored.example noisy"]:
    z.say("OperServ", t)

# Leave so seen entries get quits, then save.
for cl in (a2, e):
    try:
        cl.send("QUIT :bye for now")
    except OSError:
        pass
time.sleep(1)
z.say("OperServ", "UPDATE", 3)
print("fill done")
