"""Anope smoke test probe (2g): a user on the BeeIRCd hub uses Anope's
NickServ and ChanServ, then logs in with SASL PLAIN on a new connection.
Prints PASS/FAIL per check. Usage: anope-probe.py host port"""
import base64, re, socket, sys, time

HOST, PORT = sys.argv[1], int(sys.argv[2])
PW = "smoke-pass-2g"


class C:
    def __init__(self, nick, sasl=None):
        self.s = socket.create_connection((HOST, PORT), timeout=1)
        self.buf, self.lines, self.nick = b"", [], nick
        if sasl:
            self.send("CAP REQ :sasl")
        self.send("NICK " + nick)
        self.send("USER zt 0 * :anope smoke")
        if sasl:
            self.wait("ACK", 5)
            self.send("AUTHENTICATE PLAIN")
            self.wait(r"AUTHENTICATE \+", 8)
            self.send("AUTHENTICATE " + base64.b64encode(sasl.encode()).decode())
            self.sasl = self.wait(r" 90[3-8] ", 10)
            self.send("CAP END")
        self.wait(" 001 ", 10)

    def send(self, line):
        self.s.sendall((line + "\r\n").encode())

    def wait(self, pat, secs=5):
        end = time.time() + secs
        while time.time() < end:
            for i, l in enumerate(self.lines):
                if re.search(pat, l):
                    self.lines = self.lines[i + 1:]
                    return l
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
                    continue
                self.lines.append(l)
        return ""


ok = {}
a = C("ztsmoke")
a.send("WHOIS NickServ")
ok["NickServ is on services.sandbee.local (WHOIS 312)"] = bool(a.wait(r" 312 \S+ NickServ services\.sandbee\.local"))
a.send("PRIVMSG NickServ :REGISTER " + PW + " ztsmoke@example.org")
ok["NS REGISTER answered by Anope"] = bool(a.wait(r"NickServ!.*NOTICE ztsmoke :.*(registered|Nickname)", 8))
a.send("JOIN #smoke")
a.wait(" 366 ")
a.send("PRIVMSG ChanServ :REGISTER #smoke smoke test")
ok["CS REGISTER answered by Anope"] = bool(a.wait(r"ChanServ!.*NOTICE ztsmoke :.*registered", 8))
ok["ChanServ sets +r on the channel"] = bool(a.wait(r"MODE #smoke \+\S*r", 5))
a.send("QUIT :done")
time.sleep(1)
b = C("ztsmoke2", sasl="ztsmoke\0ztsmoke\0" + PW)
ok["SASL PLAIN via Anope (903)"] = " 903 " in (b.sasl or "")
b.send("WHOIS ztsmoke2")
ok["logged in as ztsmoke (330)"] = bool(b.wait(r" 330 \S+ ztsmoke2 ztsmoke "))
w = C("ztsmoke3", sasl="ztsmoke\0ztsmoke\0wrong-password")
ok["wrong SASL password refused (904)"] = " 904 " in (w.sasl or "")
b.send("PRIVMSG ChanServ :DROP #smoke #smoke")
b.wait("ChanServ", 5)
b.send("PRIVMSG NickServ :DROP ztsmoke")
b.wait("NickServ", 5)
for k, v in ok.items():
    print(("PASS " if v else "FAIL ") + k)
