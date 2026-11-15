package config

import "time"

// Mail is the [mail] block (Anope's mail { }): how services send email,
// for NickServ RESETPASS and MemoServ SET NOTIFY MAIL. Off by default.
type Mail struct {
	// UseMail switches mail on (Anope's usemail).
	UseMail bool `toml:"use_mail"`
	// SendFrom is the From address, like "services@example.org".
	SendFrom string `toml:"send_from"`
	// SMTPServer, like "smtp.example.org:587", sends through an SMTP server
	// (STARTTLS when it offers it). Empty: SendmailPath is used instead.
	SMTPServer   string `toml:"smtp_server"`
	SMTPUser     string `toml:"smtp_user"`
	SMTPPassword string `toml:"smtp_password"`
	// SendmailPath is the program a mail is piped into (Anope's
	// sendmailpath, "/usr/sbin/sendmail -t").
	SendmailPath string `toml:"sendmail_path"`
	// Delay is how long someone waits between two mails (Anope's delay,
	// 5m).
	Delay Duration `toml:"delay"`
	// DontQuoteAddresses writes To: name <address> without quotes.
	DontQuoteAddresses bool `toml:"dont_quote_addresses"`

	// The mails, with Anope's tokens: %n nick, %N network, %c code (reset);
	// %s sender, %d memo number, %t memo text (memo).
	ResetSubject string `toml:"reset_subject"`
	ResetMessage string `toml:"reset_message"`
	// The mail of registration = "mail" (%n nick, %N network, %c code).
	RegistrationSubject string `toml:"registration_subject"`
	RegistrationMessage string `toml:"registration_message"`
	MemoSubject         string `toml:"memo_subject"`
	MemoMessage         string `toml:"memo_message"`
}

// On reports whether services can send mail.
func (m Mail) On() bool { return m.UseMail && m.SendFrom != "" }

func (m *Mail) setDefaults() {
	if m.SendmailPath == "" {
		m.SendmailPath = "/usr/sbin/sendmail -t"
	}
	setDur(&m.Delay, 5*time.Minute)
	if m.ResetSubject == "" {
		m.ResetSubject = "Reset password request for %n"
	}
	if m.ResetMessage == "" {
		m.ResetMessage = "Hi,\n\nYou have requested to have the password for %n reset.\nTo reset your password, type \" /msg NickServ CONFIRM %n %c \"\n\nIf you don't know why this mail was sent to you, please ignore it silently.\n\n%N administrators."
	}
	if m.RegistrationSubject == "" {
		m.RegistrationSubject = "Nickname registration for %n"
	}
	if m.RegistrationMessage == "" {
		m.RegistrationMessage = "Hi,\n\nYou have requested to register the nickname %n on %N.\nPlease type \" /msg NickServ CONFIRM %c \" to complete registration.\n\nIf you don't know why this mail was sent to you, please ignore it silently.\n\n%N administrators."
	}
	if m.MemoSubject == "" {
		m.MemoSubject = "New memo"
	}
	if m.MemoMessage == "" {
		m.MemoMessage = "Hi %n,\n\nYou've just received a new memo from %s. This is memo number %d.\n\nMemo text:\n\n%t"
	}
}
