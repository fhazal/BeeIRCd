"""irctest controller for BeeIRCd.

Copied into irctest/controllers/ by scripts/irctest.sh; not part of the
server. It starts a throwaway BeeIRCd in irctest's temp directory with its
own ports and database, so nothing outside the test run is touched.
"""

import socket
import subprocess
import time
from typing import Optional, Set, Type

from irctest.basecontrollers import BaseServerController, DirectoryBasedController
from irctest.runner import NotImplementedByController

TEMPLATE_CONFIG = """
[server]
name = "My.Little.Server"
network = "BeeTest"
description = "test server"
sid = "0IT"
resolve_hosts = false
ircv3_drafts = true

[[listen]]
address = "{hostname}:{port}"
tls = {tls}

[tls]
cert = "tls/cert.pem"
key = "tls/key.pem"
self_signed = true

[motd]
text = "irctest"

[cloak]
enabled = false
secret = "irctest-secret-0123456789"

[limits]
flood_burst = 10000
flood_rate = 10000
session_limit = 1000
nick_length = 32

[channels]
flood_profile = "off"

{history}
[services]
database = "services.db"
admin = "operuser"
admin_password = "{admin_hash}"
reg_delay = "0s"
force_email = false
"""


class BeeIRCdController(BaseServerController, DirectoryBasedController):
    software_name = "BeeIRCd"
    supported_sasl_mechanisms: Set[str] = {"PLAIN"}
    supports_sts = False
    extban_mute_char = "m"

    def create_config(self) -> None:
        super().create_config()
        with self.open_file("beeircd.conf"):
            pass

    def run(
        self,
        hostname: str,
        port: int,
        *,
        password: Optional[str],
        ssl: bool,
        run_services: bool,
        faketime: Optional[str],
        websocket_hostname: Optional[str],
        websocket_port: Optional[int],
    ) -> None:
        if websocket_hostname is not None or websocket_port is not None:
            raise NotImplementedByController("Websocket")
        if password is not None:
            # BeeIRCd has no server password (PASS is accepted and ignored).
            raise NotImplementedByController("PASS")
        assert self.proc is None
        self.port = port
        self.hostname = hostname
        self.create_config()
        assert self.directory
        admin_hash = subprocess.run(
            ["beeircd", "mkpasswd"], input="operpassword\n", capture_output=True, text=True, check=True
        ).stdout.strip()
        with self.open_file("beeircd.conf") as fd:
            fd.write(
                TEMPLATE_CONFIG.format(
                    hostname=hostname,
                    port=port,
                    tls="true" if ssl else "false",
                    admin_hash=admin_hash,
                    history=self._history_conf(),
                )
            )
        self.proc = self.execute(
            ["beeircd", "run", "-config", self.directory / "beeircd.conf"]
        )
        self.wait_for_port()
        if not ssl:
            # irctest opers up as operuser/operpassword: BeeIRCd opers are
            # registered accounts, so register the config admin first.
            self._raw_register("operuser", "irctest-account-pw")

    def _history_conf(self) -> str:
        # Chathistory tests get +H on every new channel; others keep the
        # plain modes. Playback on join is off: it isn't in any spec.
        cfg = getattr(self, "test_config", None)
        if cfg is None or not getattr(cfg, "chathistory", False):
            return ""
        return '[history]\ndefault = "1000:1d"\nplayback_lines = 0\n'

    def wait_for_services(self) -> None:
        # Services are built in: they are up when the port is.
        pass

    def _raw_register(self, nick: str, password: str) -> None:
        s = socket.create_connection((self.hostname, self.port), timeout=5)
        f = s.makefile("rb")
        s.sendall(f"NICK {nick}\r\nUSER {nick} 0 * :{nick}\r\n".encode())
        deadline = time.time() + 10
        sent = False
        while time.time() < deadline:
            line = f.readline().decode(errors="replace")
            if not line:
                break
            if line.startswith("PING"):
                s.sendall(("PONG" + line[4:]).encode())
            if " 001 " in line and not sent:
                s.sendall(f"PRIVMSG NickServ :REGISTER {password}\r\n".encode())
                sent = True
            if "is now registered" in line:
                break
        s.sendall(b"QUIT\r\n")
        s.close()

    def registerUser(self, case, username: str, password: Optional[str] = None) -> None:  # type: ignore
        assert password
        client = case.addClient(show_io=False)
        case.sendLine(client, "NICK " + username)
        case.sendLine(client, "USER r e g :user")
        while case.getRegistrationMessage(client).command != "001":
            pass
        case.getMessages(client)
        case.sendLine(client, "PRIVMSG NickServ :REGISTER " + password)
        # The password is hashed off the main loop (argon2), so the reply
        # comes after later PINGs: wait in real time, not by PING sync.
        deadline = time.time() + 10
        while time.time() < deadline:
            msgs = case.getMessages(client, synchronize=False)
            if any("is now registered" in (m.params[-1] if m.params else "") for m in msgs):
                break
            time.sleep(0.05)
        else:
            raise AssertionError("NickServ did not register " + username)
        case.sendLine(client, "QUIT")
        case.assertDisconnected(client)


def get_irctest_controller_class() -> Type[BeeIRCdController]:
    return BeeIRCdController
