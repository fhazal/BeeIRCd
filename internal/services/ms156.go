package services

import (
	crand "crypto/rand"
	"crypto/tls"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/smtp"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Step 1.5.6b: MemoServ CANCEL, CHECK, INFO, SENDALL, STAFF and SET
// NOTIFY as Anope 2.0.20 has them; mail (Anope's mail { } block) for
// NickServ RESETPASS and SET NOTIFY MAIL; NickServ BUDDY, a ConferenceRoom
// extra on top.

func ms156Commands() map[string]svcCmd {
	return map[string]svcCmd{
		"CANCEL": {"CANCEL {<nick> | #chan}", "take back your last unread memo",
			"Cancels the last memo you sent to the given nick or channel,\nprovided it has not been read at the time you use the command.", msCancel},
		"CHECK": {"CHECK <nick>", "was your last memo read",
			"Checks whether the _last_ memo you sent to nick has been read\nor not. Note that this only works with nicks, not with channels.", msCheck},
		"INFO": {"INFO [nick | #chan]", "how many memos, the limit, notices",
			"Without a parameter, displays information on the number of\nmemos you have, how many of them are unread, and how many\ntotal memos you can receive.\nWith a channel parameter, displays the same information for\nthe given channel (MEMO privilege).\nOpers may give a nick to see that person's.", msInfo},
		"SENDALL": {"SENDALL <text>", "a memo to every account (opers)",
			"Sends all registered users a memo containing the text. Oper flag g.", msSendAll},
		"STAFF": {"STAFF <text>", "a memo to every oper (opers)",
			"Sends all services staff a memo containing the text.", msStaff},
	}
}

func ns156bCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"RESETPASS": {"RESETPASS <nick> <email>", "get a code to reset a lost password",
			"Sends a passcode to the nickname with instructions on how to\nreset their password. The email must be the one the nickname\nis registered with. Then: /ns confirm <nick> <passcode>", nsResetPass},
		"CONFIRM": {"CONFIRM <nick> <passcode>", "use the code RESETPASS sent you",
			"Identifies you to the nick with the passcode a RESETPASS mail gave you, so\nyou can set a new password: /ns set password <new password>", nsConfirm},
		"BUDDY": {"BUDDY {ADD | DEL | LIST} [nick]", "friends you hear about",
			"BUDDY ADD <nick>\nBUDDY DEL <nick>\nBUDDY LIST\nYour list of friends, kept with your account (up to 32). NickServ tells you\nwhen one of them identifies or goes offline, and who is online when you\nidentify. (Like ConferenceRoom's BUDDY; MONITOR does the same in clients\nthat know it.)", nsBuddy},
	}
}

const maxBuddies = 32

// --- mail ---

// Mailer sends one mail; tests replace it.
type Mailer func(name, addr, subject, body string) error

// SetMailer replaces how mail is sent (for tests).
func (s *Services) SetMailer(m Mailer) { s.mailer = m }

// mailTo sends a mail to account a. With c (someone asking), the reasons
// it can't are told to them, as Anope's Mail::Send(User, ...) does, and
// they wait mail.delay between mails.
func (s *Services) mailTo(c *Client, sv *service, a *store.Account, subject, body string) bool {
	m := s.cfg.Mail
	now := time.Now()
	if c != nil {
		switch {
		case !m.On():
			s.svcReply(c, sv, "Services have been configured to not send mail.")
			return false
		case now.Sub(c.lastMail) < m.Delay.Duration:
			s.svcReply(c, sv, fmt.Sprintf("Please wait %d seconds and retry.", int(math.Ceil((m.Delay.Duration-now.Sub(c.lastMail)).Seconds()))))
			return false
		case a.Email == "":
			s.svcReply(c, sv, fmt.Sprintf("E-mail for %s is invalid.", a.Name))
			return false
		}
		c.lastMail = now
	} else if !m.On() || a.Email == "" {
		return false
	}
	send := s.mailer
	if send == nil {
		send = s.sendMail
	}
	name, addr := a.Name, a.Email
	go func() {
		err := send(name, addr, subject, body)
		s.post(func() {
			if err != nil {
				s.logTo(s.global, "Error delivering mail for "+name+" ("+addr+"): "+err.Error())
			} else {
				s.logTo(s.global, "Successfully delivered mail for "+name+" ("+addr+")")
			}
		})
	}()
	return true
}

// mailText builds the whole mail as Anope's Mail::Message writes it.
func (s *Services) mailText(name, addr, subject, body string) string {
	m := s.cfg.Mail
	to := "\"" + strings.ReplaceAll(name, "\\", "\\\\") + "\" <" + addr + ">"
	if m.DontQuoteAddresses {
		to = name + " <" + addr + ">"
	}
	return "From: " + m.SendFrom + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
}

// sendMail delivers through the SMTP server, or the sendmail program.
// It runs off the loop.
func (s *Services) sendMail(name, addr, subject, body string) error {
	m := s.cfg.Mail
	text := s.mailText(name, addr, subject, body)
	if m.SMTPServer == "" {
		f := strings.Fields(m.SendmailPath)
		if len(f) == 0 {
			return fmt.Errorf("no sendmail_path")
		}
		cmd := exec.Command(f[0], f[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	host, _, err := net.SplitHostPort(m.SMTPServer)
	if err != nil {
		return err
	}
	cl, err := smtp.Dial(m.SMTPServer)
	if err != nil {
		return err
	}
	defer cl.Close()
	if ok, _ := cl.Extension("STARTTLS"); ok {
		if err := cl.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if m.SMTPUser != "" {
		if err := cl.Auth(smtp.PlainAuth("", m.SMTPUser, m.SMTPPassword, host)); err != nil {
			return err
		}
	}
	if err := cl.Mail(m.SendFrom); err != nil {
		return err
	}
	if err := cl.Rcpt(addr); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(text)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// fill replaces Anope's mail tokens.
func fill(text string, tokens map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '%' && i+1 < len(text) {
			if v, ok := tokens[text[i:i+2]]; ok {
				b.WriteString(v)
				i++
				continue
			}
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

// memoMail tells a by mail about memo number num (SET NOTIFY MAIL).
func (s *Services) memoMail(a *store.Account, from, text string, num int) {
	if !a.MemoMail {
		return
	}
	t := map[string]string{"%n": a.Name, "%s": from, "%d": strconv.Itoa(num), "%t": text, "%N": s.cfg.Server.Network}
	s.mailTo(nil, s.memoServ, a, fill(s.cfg.Mail.MemoSubject, t), fill(s.cfg.Mail.MemoMessage, t))
}

// --- NickServ RESETPASS and CONFIRM ---

type resetInfo struct {
	code string
	at   time.Time
}

func nsResetPass(s *Services, c *Client, args []string) {
	if len(args) < 2 {
		s.ns(c, "Syntax: RESETPASS <nick> <email>")
		return
	}
	a, err := s.store.AccountByNick(args[0])
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	switch {
	case a == nil:
		s.ns(c, "Nick %s isn't registered.", args[0])
	case a.Suspended(time.Now()):
		s.ns(c, "Nick %s is suspended.", a.Name)
	case !strings.EqualFold(args[1], a.Email):
		s.ns(c, "Incorrect email address.")
	default:
		code := randomPasscode(20)
		t := map[string]string{"%n": args[0], "%N": s.cfg.Server.Network, "%c": code}
		if s.mailTo(c, s.nickServ, a, fill(s.cfg.Mail.ResetSubject, t), fill(s.cfg.Mail.ResetMessage, t)) {
			s.resets[a.ID] = resetInfo{code, time.Now()}
			s.nsLogCmd(c, "for %s (group: %s)", args[0], a.Name)
			s.ns(c, "Password reset email for %s has been sent.", args[0])
		}
	}
}

func nsConfirm(s *Services, c *Client, args []string) {
	if len(args) < 2 {
		s.ns(c, "Syntax: CONFIRM <nick> <passcode>")
		return
	}
	a, err := s.store.AccountByNick(args[0])
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	ri, ok := resetInfo{}, false
	if a != nil {
		ri, ok = s.resets[a.ID]
	}
	switch {
	case !ok:
		s.ns(c, "Invalid passcode.")
	case a.Suspended(time.Now()):
		s.ns(c, "Nick %s is suspended.", a.Name)
	case time.Since(ri.at) > time.Hour:
		delete(s.resets, a.ID)
		s.ns(c, "Your password reset request has expired.")
	case args[1] != ri.code:
		s.ns(c, "Invalid passcode.")
	default:
		delete(s.resets, a.ID)
		delete(s.lockouts, a.ID)
		s.nsLogCmd(c, "to confirm RESETPASS and forcefully identify as %s", args[0])
		s.login(c, a)
		s.ns(c, "You are now identified for your nick. Change your password now.")
	}
}

// randomPasscode is Anope::Random: letters and digits. It logs someone
// in (CONFIRM), so it comes from crypto/rand.
func randomPasscode(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		k, err := crand.Int(crand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			panic("crypto/rand: " + err.Error()) // never happens on a working OS
		}
		b[i] = chars[k.Int64()]
	}
	return string(b)
}

// --- MemoServ ---

func msCancel(s *Services, c *Client, args []string) {
	from, ok := s.msIdentified(c)
	if !ok {
		return
	}
	target := argAt(args, 0)
	if target == "" {
		s.ms(c, "Syntax: CANCEL {<nick> | #chan}")
		return
	}
	if irc.IsChannel(target) {
		reg, _ := s.store.ChannelByName(target)
		if reg == nil {
			s.ms(c, "Channel %s isn't registered.", target)
			return
		}
		memos := s.store.ChanMemos(reg)
		for i := len(memos) - 1; i >= 0; i-- {
			if !memos[i].Read && s.senderIs(memos[i].From, from) {
				if err := s.store.DeleteChanMemo(memos[i].ID); err != nil {
					s.dbErr(c, s.memoServ, err)
					return
				}
				s.ms(c, "Last memo to %s has been cancelled.", reg.Name)
				return
			}
		}
		s.ms(c, "No memo was cancelable.")
		return
	}
	to, _ := s.store.AccountByNick(target)
	if to == nil {
		s.ms(c, "Nick %s isn't registered.", target)
		return
	}
	memos, _ := s.store.Memos(to)
	for i := len(memos) - 1; i >= 0; i-- {
		if !memos[i].Read && s.senderIs(memos[i].From, from) {
			if err := s.store.DeleteMemo(memos[i].ID); err != nil {
				s.dbErr(c, s.memoServ, err)
				return
			}
			s.ms(c, "Last memo to %s has been cancelled.", to.Name)
			return
		}
	}
	s.ms(c, "No memo was cancelable.")
}

// senderIs reports whether a memo's sender (a nick) belongs to account a.
func (s *Services) senderIs(sender string, a *store.Account) bool {
	sa, _ := s.store.AccountByNick(sender)
	return sa != nil && sa.ID == a.ID
}

func msCheck(s *Services, c *Client, args []string) {
	from, ok := s.msIdentified(c)
	if !ok {
		return
	}
	nick := argAt(args, 0)
	to, _ := s.store.AccountByNick(nick)
	if nick == "" || to == nil {
		s.ms(c, "Nick %s isn't registered.", nick)
		return
	}
	memos, _ := s.store.Memos(to)
	for i := len(memos) - 1; i >= 0; i-- {
		m := memos[i]
		if !s.senderIs(m.From, from) {
			continue
		}
		if m.Read {
			s.ms(c, "The last memo you sent to %s (sent on %s) has been read.", nick, anopeTime(m.Sent))
		} else {
			s.ms(c, "The last memo you sent to %s (sent on %s) has not yet been read.", nick, anopeTime(m.Sent))
		}
		return
	}
	s.ms(c, "Nick %s doesn't have a memo from you.", nick)
}

func msInfo(s *Services, c *Client, args []string) {
	me, ok := s.msIdentified(c)
	if !ok {
		return
	}
	target := argAt(args, 0)
	var memos []store.Memo
	limit, own, name := me.MemoLimit, true, ""
	var a *store.Account
	switch {
	case target == "":
		a = me
		memos, _ = s.store.Memos(me)
	case irc.IsChannel(target):
		reg, _ := s.store.ChannelByName(target)
		if reg == nil {
			s.ms(c, "Channel %s isn't registered.", target)
			return
		}
		if !s.levelHas(reg, s.accountLevel(reg, c.accountID), "MEMO") {
			s.ms(c, "Access denied.")
			return
		}
		memos, limit, own, name = s.store.ChanMemos(reg), reg.MemoLimit, false, reg.Name
	default:
		if !c.isOper() {
			s.ms(c, "Access denied.")
			return
		}
		a, _ = s.store.AccountByNick(target)
		if a == nil {
			s.ms(c, "Nick %s isn't registered.", target)
			return
		}
		memos, _ = s.store.Memos(a)
		limit, own, name = a.MemoLimit, a.ID == me.ID, target
	}
	unread := 0
	for _, m := range memos {
		if !m.Read {
			unread++
		}
	}
	who, has := "You", "You currently have"
	if !own {
		who, has = name, name+" currently has"
	}
	switch n := len(memos); {
	case n == 0:
		s.ms(c, "%s no memos.", has)
	case n == 1 && unread == 1:
		s.ms(c, "%s 1 memo, and it has not yet been read.", has)
	case n == 1:
		s.ms(c, "%s 1 memo.", has)
	case unread == n:
		s.ms(c, "%s %d memos; all of them are unread.", has, n)
	case unread == 0:
		s.ms(c, "%s %d memos.", has, n)
	case unread == 1:
		s.ms(c, "%s %d memos, of which 1 is unread.", has, n)
	default:
		s.ms(c, "%s %d memos, of which %d are unread.", has, n, unread)
	}
	if own {
		s.ms(c, "Your memo limit is %d.", limit)
	} else {
		s.ms(c, "%s's memo limit is %d.", name, limit)
	}
	if a == nil {
		return
	}
	switch {
	case own && a.MemoSignon && a.MemoNotify && a.MemoMail:
		s.ms(c, "You will be notified of new memos at logon and when they arrive, and by mail when they arrive.")
	case own && a.MemoSignon && a.MemoNotify:
		s.ms(c, "You will be notified of new memos at logon and when they arrive.")
	case own && a.MemoNotify && a.MemoMail:
		s.ms(c, "You will be notified by message and by mail when new memos arrive.")
	case own && a.MemoNotify:
		s.ms(c, "You will be notified when new memos arrive.")
	case own && a.MemoSignon && a.MemoMail:
		s.ms(c, "You will be notified of new memos at logon, and by mail when they arrive.")
	case own && a.MemoSignon:
		s.ms(c, "You will be notified of new memos at logon.")
	case own:
		s.ms(c, "You will not be notified of new memos.")
	case a.MemoSignon && a.MemoNotify:
		s.ms(c, "%s is notified of new memos at logon and when they arrive.", who)
	case a.MemoNotify:
		s.ms(c, "%s is notified when new memos arrive.", who)
	case a.MemoSignon:
		s.ms(c, "%s is notified of new memos at logon.", who)
	default:
		s.ms(c, "%s is not notified of new memos.", who)
	}
}

// deliverMemo puts a memo in a's box (forced past the limit and ignores
// for STAFF and SENDALL, as Anope's Send(force)) and tells them.
func (s *Services) deliverMemo(a *store.Account, from, text string) {
	if err := s.store.AddMemo(a, from, text, time.Now()); err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	memos, _ := s.store.Memos(a)
	if a.MemoNotify {
		for _, oc := range s.onlineClients(a.ID) {
			s.ms(oc, "You have a new memo from %s. Read it: /ms read %d", from, len(memos))
		}
	}
	s.memoMail(a, from, text, len(memos))
}

func msSendAll(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.memoServ, 'g') {
		return
	}
	text := strings.Join(args, " ")
	if text == "" {
		s.ms(c, "Syntax: SENDALL <text>")
		return
	}
	s.logCmd(c, s.memoServ, logAdmin, "", "to send "+text)
	for _, a := range s.store.AllAccounts() {
		if a.ID != c.accountID {
			s.deliverMemo(a, c.user.Nick, text)
		}
	}
	s.ms(c, "A massmemo has been sent to all registered users.")
}

func msStaff(s *Services, c *Client, args []string) {
	if !c.isOper() {
		s.ms(c, "Access denied.")
		return
	}
	text := strings.Join(args, " ")
	if text == "" {
		s.ms(c, "Syntax: STAFF <text>")
		return
	}
	n := 0
	for _, a := range s.store.AllAccounts() {
		if a.ID != c.accountID && (s.isServicesOper(a) || s.isAdminAccount(a)) {
			s.deliverMemo(a, c.user.Nick, text)
			n++
		}
	}
	// Anope 2.0.20 answers nothing here; we say it went.
	s.ms(c, "A memo has been sent to all services staff (%s).", plural(n, "account"))
}

// msSetNotify is SET NOTIFY {ON | LOGON | NEW | MAIL | NOMAIL | OFF}.
func (s *Services) msSetNotify(c *Client, a *store.Account, val string) {
	mailOn := s.cfg.Mail.On()
	var msg string
	switch strings.ToUpper(val) {
	case "ON":
		a.MemoSignon, a.MemoNotify = true, true
		msg = "MemoServ will now notify you of memos when you log on and when they are sent to you."
	case "LOGON":
		a.MemoSignon, a.MemoNotify = true, false
		msg = "MemoServ will now notify you of memos when you log on or unset /AWAY."
	case "NEW":
		a.MemoSignon, a.MemoNotify = false, true
		msg = "MemoServ will now notify you of memos when they are sent to you."
	case "MAIL":
		if !mailOn {
			s.ms(c, "Syntax: SET NOTIFY {ON | LOGON | NEW | OFF}")
			return
		}
		if a.Email == "" {
			s.ms(c, "There's no email address set for your nick.")
			return
		}
		a.MemoMail = true
		msg = "You will now be informed about new memos via email."
	case "NOMAIL":
		if !mailOn {
			s.ms(c, "Syntax: SET NOTIFY {ON | LOGON | NEW | OFF}")
			return
		}
		a.MemoMail = false
		msg = "You will no longer be informed via email."
	case "OFF":
		a.MemoSignon, a.MemoNotify, a.MemoMail = false, false, false
		msg = "MemoServ will not send you any notification of memos."
	default:
		if mailOn {
			s.ms(c, "Syntax: SET NOTIFY {ON | LOGON | NEW | MAIL | NOMAIL | OFF}")
		} else {
			s.ms(c, "Syntax: SET NOTIFY {ON | LOGON | NEW | OFF}")
		}
		return
	}
	if err := s.store.SaveAccount(a); err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	s.ms(c, "%s", msg)
}

// --- NickServ BUDDY (ConferenceRoom) ---

func nsBuddy(s *Services, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	sub, nick := strings.ToUpper(argAt(args, 0)), argAt(args, 1)
	switch {
	case sub == "LIST" || sub == "":
		list := s.store.Buddies(a)
		if len(list) == 0 {
			s.ns(c, "Your buddy list is empty. Add someone: /ns buddy add <nick>")
			return
		}
		s.ns(c, "Your buddies:")
		for _, b := range list {
			state := "offline"
			if s.accountOnline(b) {
				state = "online"
			}
			s.ns(c, "  %-20s %s", b, state)
		}
	case (sub == "ADD" || sub == "DEL") && nick == "":
		s.ns(c, "Syntax: BUDDY %s <nick>", sub)
	case sub == "ADD":
		if len(s.store.Buddies(a)) >= maxBuddies {
			s.ns(c, "Your buddy list is full (%d).", maxBuddies)
			return
		}
		b, _ := s.store.AccountByNick(nick)
		if b == nil {
			s.ns(c, "Nick %s isn't registered.", nick)
			return
		}
		if b.ID == a.ID {
			s.ns(c, "%s is one of your own nicks.", nick)
			return
		}
		added, err := s.store.AddBuddy(a, nick)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		if !added {
			s.ns(c, "%s is on your buddy list already.", nick)
			return
		}
		s.ns(c, "%s added to your buddy list.", nick)
	case sub == "DEL":
		gone, err := s.store.DelBuddy(a, nick)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		if !gone {
			s.ns(c, "%s isn't on your buddy list.", nick)
			return
		}
		s.ns(c, "%s removed from your buddy list.", nick)
	default:
		s.ns(c, "Syntax: BUDDY {ADD | DEL | LIST} [nick]")
	}
}

// accountOnline reports whether someone is identified to the account
// owning nick.
func (s *Services) accountOnline(nick string) bool {
	a, _ := s.store.AccountByNick(nick)
	return a != nil && len(s.onlineClients(a.ID)) > 0
}

// buddyWatchers are the connections whose BUDDY list has a nick of a.
func (s *Services) buddyWatchers(a *store.Account) []*Client {
	nicks, _ := s.store.Nicks(a)
	seen := map[int64]bool{}
	var out []*Client
	for _, n := range nicks {
		for _, id := range s.store.BuddyOf(n) {
			if seen[id] || id == a.ID {
				continue
			}
			seen[id] = true
			out = append(out, s.onlineClients(id)...)
		}
	}
	return out
}

// buddyOnline runs when c identified to a: their buddies hear it (unless
// a was online already), and c hears which buddies are on.
func (s *Services) buddyOnline(c *Client, a *store.Account) {
	if len(s.onlineClients(a.ID)) == 1 {
		for _, w := range s.buddyWatchers(a) {
			s.ns(w, "Your buddy %s is now online.", a.Name)
		}
	}
	var on []string
	for _, b := range s.store.Buddies(a) {
		if s.accountOnline(b) {
			on = append(on, b)
		}
	}
	if len(on) > 0 {
		s.ns(c, "Buddies online: %s", strings.Join(on, ", "))
	}
}

// buddyOffline runs after account id lost a connection: when none is
// left, its buddies hear it.
func (s *Services) buddyOffline(id int64) {
	if id == 0 || len(s.onlineClients(id)) > 0 {
		return
	}
	a, _ := s.store.AccountByID(id)
	if a == nil {
		return
	}
	for _, w := range s.buddyWatchers(a) {
		s.ns(w, "Your buddy %s has gone offline.", a.Name)
	}
}
