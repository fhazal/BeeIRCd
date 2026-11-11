package main

import (
	"strings"
	"text/template"
)

// Where `beeircd install` puts things (SPEC §3.1).
const (
	instBin     = "/opt/beeircd/beeircd"
	instLink    = "/usr/local/bin/beeircd"
	instEtc     = "/etc/beeircd"
	instConf    = "/etc/beeircd/beeircd.conf"
	instMOTD    = "/etc/beeircd/motd.txt"
	instTLS     = "/etc/beeircd/tls"
	instData    = "/var/lib/beeircd"
	instSocket  = "/run/beeircd/beeircd.sock"
	instUnit    = "/etc/systemd/system/beeircd.service"
	instBkUnit  = "/etc/systemd/system/beeircd-backup.service"
	instBkTimer = "/etc/systemd/system/beeircd-backup.timer"
	instLEHook  = "/etc/letsencrypt/renewal-hooks/deploy/beeircd"
	instUser    = "beeircd"
)

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

func tomlList(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = tomlString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

var tmplFuncs = template.FuncMap{"q": tomlString, "list": tomlList}

// confTemplate is the beeircd.conf the installer writes: every block
// explained, so the file is the manual for the settings in it.
var confTemplate = template.Must(template.New("conf").Funcs(tmplFuncs).Parse(`# BeeIRCd configuration, written by ` + "`beeircd install`" + ` on {{.Date}}.
#
# After a change: ` + "`beeircd check`" + `, then ` + "`beeircd rehash`" + ` (most settings apply
# live; it lists the ones that need ` + "`beeircd restart`" + `). AdminServ on IRC
# writes to this file too and keeps these comments.

[server]
# The name people connect to and see as the server. Changing it needs a restart.
name = {{q .Name}}
network = {{q .Network}}
description = {{q .Description}}
# Server ID for linking servers later (Stage 2): a digit and two capital
# letters or digits, unique on the network.
sid = {{q .SID}}
# Channel status style: "@+" (op and voice) or "~&@%+" (owner, admin, op,
# halfop, voice). Changing it needs a restart.
prefixes = {{q .Prefixes}}

# One block per port. tls = true for encrypted ports.
{{- if .PlainPort}}

[[listen]]
address = ":{{.PlainPort}}"
{{- end}}

[[listen]]
address = ":{{.TLSPort}}"
tls = true

[tls]
{{- if eq .Cert "acme"}}
# Let's Encrypt: BeeIRCd gets the certificate itself and renews it 30 days
# before it runs out. Port 80 of {{.Name}} must reach this machine (it is
# opened only while a certificate is asked for).
acme = true
{{- if .ACMEEmail}}
acme_email = {{q .ACMEEmail}}
{{- end}}
cert = {{q .CertFile}}
key = {{q .KeyFile}}
{{- else if eq .Cert "self"}}
# A self-signed certificate, made at the first start. Clients will warn
# until you switch to Let's Encrypt (acme = true) or your own files.
cert = {{q .CertFile}}
key = {{q .KeyFile}}
self_signed = true
{{- else}}
# Your own certificate files. A renewed file is picked up within a minute.
cert = {{q .CertFile}}
key = {{q .KeyFile}}
{{- end}}

[motd]
# Shown to everyone who connects; edit the file, then ` + "`beeircd rehash`" + `.
file = {{q .MOTDFile}}

[cloak]
# Hides people's IPs (user mode +x). Keep the secret: every linked server
# needs the same one, and changing it changes everyone's cloak.
secret = {{q .CloakSecret}}

[services]
database = {{q .Database}}
# The admin has every oper flag: /oper {{.Admin}} <password>.
admin = {{q .Admin}}
# The hash of the admin's oper password (` + "`beeircd mkpasswd`" + ` makes a new one).
admin_password = {{q .AdminHash}}
# Where the admin may /oper from (user@host masks or CIDR). Empty = anywhere.
admin_hosts = {{list .AdminHosts}}

[history]
# Channel history (+H). New channels keep the last 50 lines for a day.
default = "50:1d"
# Encrypts history.db; keep it secret, a new one makes old history unreadable.
secret = {{q .HistorySecret}}

[control]
# The socket ` + "`beeircd status`" + `, ` + "`backup`" + ` and ` + "`rehash`" + ` use (no network port).
socket = {{q .Socket}}

[link]
# This server's place in a network: "standalone", "hub" (other servers link
# to it) or "leaf" (it links to a hub). Server linking arrives in Stage 2;
# until then this is only stored. The link port is TLS only.
role = {{q .Role}}
{{- if .LinkPort}}
port = {{.LinkPort}}
{{- end}}

[update]
# ` + "`beeircd update`" + ` installs new releases; each one is signed and checked
# first. The server looks once a day for a new one (one small download,
# nothing about this server is sent); check = false turns that off.
check = true

[backup]
# ` + "`beeircd backup`" + ` writes here; the beeircd-backup timer makes one daily
# at 04:00 ({{.TimeZone}}). The newest 7 are kept.
dir = {{q .BackupDir}}
keep = 7
`))

var motdText = `Welcome to {{.Network}}!
This server runs BeeIRCd. Be kind, stay curious.
`

// unitTemplate is the systemd service, hardened: the server can write only
// its config folder and data, sees no home folders, and gets the right to
// open ports under 1024 only when it needs it.
var unitTemplate = template.Must(template.New("unit").Parse(`# Written by beeircd install. ` + "`beeircd uninstall`" + ` removes it.
[Unit]
Description=BeeIRCd IRC server
Documentation=https://beeircd.org
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=beeircd
Group=beeircd
ExecStart=/opt/beeircd/beeircd run -config /etc/beeircd/beeircd.conf
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
LimitNOFILE=65536
RuntimeDirectory=beeircd
RuntimeDirectoryMode=0750
UMask=0027
{{- if .LowPorts}}
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
{{- else}}
CapabilityBoundingSet=
{{- end}}
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/etc/beeircd /var/lib/beeircd
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`))

const backupUnit = `# Written by beeircd install: the daily backup (beeircd-backup.timer).
[Unit]
Description=BeeIRCd daily backup
After=beeircd.service
Requisite=beeircd.service

[Service]
Type=oneshot
User=beeircd
Group=beeircd
ExecStart=/opt/beeircd/beeircd backup -config /etc/beeircd/beeircd.conf
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/beeircd
ProtectHome=yes
PrivateTmp=yes
`

const backupTimer = `# Written by beeircd install.
[Unit]
Description=BeeIRCd daily backup at 04:00

[Timer]
OnCalendar=*-*-* 04:00:00
Persistent=true
RandomizedDelaySec=5m

[Install]
WantedBy=timers.target
`

// leHook copies a certbot certificate to where the beeircd user can read
// it after each renewal; BeeIRCd notices the new file within a minute.
var leHookTemplate = template.Must(template.New("hook").Parse(`#!/bin/sh
# Written by beeircd install: certbot runs this after renewing
# {{.Live}}. BeeIRCd picks the copy up within a minute.
set -e
case "$RENEWED_LINEAGE" in {{.Live}}) ;; *) exit 0 ;; esac
install -o beeircd -g beeircd -m 0644 {{.Live}}/fullchain.pem {{.Cert}}
install -o beeircd -g beeircd -m 0600 {{.Live}}/privkey.pem {{.Key}}
`))
