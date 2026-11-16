# Writing modules

A module adds something of your own to BeeIRCd: a greeting, a command, a word filter. It is one Lua file. You don't need a compiler, and you load it while the server runs.

This is an early version of modules. It has the hooks and functions listed here; more are planned (see [What is planned](#what-is-planned)).

## A first module

```sh
beeircd module new greet
```

writes `greet.lua` into the modules folder (`modules` next to `beeircd.conf`):

```lua
module { name = "greet", version = "0.1", author = "", about = "What this module does" }

local text = config.text or "Hello"

on("join", function(e)
  if e.channel.name == "#lobby" then
    notice(e.user, text .. ", " .. e.user.nick .. "!")
  end
end)

command("HELLO", { params = 1, help = "HELLO <nick>: says hello to someone" }, function(user, args)
  local to = find_user(args[1])
  if not to then
    return fail(user, "No such nick: " .. args[1])
  end
  notice(to, user.nick .. " says hello")
end)
```

Check it, then load it on IRC as an oper:

```sh
beeircd module check greet.lua
```

```
/MODULE LOAD greet
```

To keep it after a restart, list it in `beeircd.conf`:

```toml
[modules]
load = ["greet"]

[module.greet]
text = "Welcome to the hive"
```

## Managing modules

| On IRC (opers with flag `a`) | Does |
| --- | --- |
| `/MODULE LIST` | The loaded modules |
| `/MODULE INFO greet` | Version, author, hooks, commands, rights, calls and errors |
| `/MODULE LOAD greet` | Loads `greet.lua` from the modules folder |
| `/MODULE RELOAD greet` | Reads the file again (after you changed it) |
| `/MODULE UNLOAD greet` | Takes it out |

`/REHASH` loads modules added to `[modules] load`, unloads the ones taken out, and gives every module its fresh `[module.NAME]` settings.

| On the command line | Does |
| --- | --- |
| `beeircd module new <name>` | Writes a starter module |
| `beeircd module check <file.lua>` | Finds mistakes without a server: syntax, unknown hooks, a name that does not match the file |
| `beeircd module hooks` | Lists every hook |

## The parts of a module

### The header

```lua
module { name = "greet", version = "1.0", author = "You", about = "One line", api = 1 }
```

`name` must be the file name without `.lua` (small letters, digits, `-` and `_`). `api` is the module interface the module was written for; leave it out for 1.

### Settings

`config` is the module's `[module.NAME]` block: `config.text`, `config.limit`, lists and sub-tables as written there. It is fresh after every `/REHASH`.

### Hooks

`on("name", function(e) ... end)` registers a hook. `e` is a table with what it is about.

Told after it happened (these can't stop anything, and run for people on every server):

| Hook | `e` has |
| --- | --- |
| `connect` | `user` |
| `quit` | `user`, `reason` |
| `nick` | `user` (with the new nick), `old` |
| `join` | `user`, `channel`, `created` (true for the first one in), `burst` (true while servers link) |
| `part` | `user`, `channel`, `reason` |
| `kick` | `user` (who was kicked), `channel`, `source` (who did it, nil for the server), `reason` |
| `topic` | `channel`, `text`, `source` |
| `channel_mode` | `channel`, `modes` (like `+o-v ann bob`), `source` |
| `user_mode` | `user`, `modes` (like `+w`) |
| `away`, `back` | `user`, `text` |
| `login`, `logout` | `user`, `account` |
| `host_change`, `realname_change` | `user`, `old` |
| `invite` | `user` (who was invited), `channel`, `source` |
| `message` | `user`, `channel` or `target`, `text`, `notice`; only for people on this server |
| `rehash` | nothing; `config` is fresh |
| `tick` | nothing; once a second |
| `load`, `unload` | nothing; this module started or is about to stop |

Asked before it happens. The function may:

- return nothing: it goes ahead;
- return a text: it is refused, and the person is told that text;
- return `false`: it is refused without a reason of your own;
- set one of the fields marked "can change": it goes ahead with the new value.

| Hook | `e` has | Can change |
| --- | --- | --- |
| `check_connect` | `user` (someone about to be let in; refusing disconnects them) | – |
| `check_nick` | `user`, `nick` (the nick they want) | – |
| `check_join` | `user`, `name`, `channel` (nil when it does not exist yet) | – |
| `check_create` | `user`, `name` (a channel that would be made) | – |
| `check_message` | `user`, `channel` or `target`, `text`, `notice` | `text`; `false` drops it without a word |
| `check_tagmsg` | `user`, `channel` or `target` (typing notes, reactions) | – ; always refused without a word |
| `check_part` | `user`, `channel`, `reason` | `reason`; refusing only removes the message |
| `check_quit` | `user`, `reason` | `reason`; refusing only removes the message |
| `check_topic` | `user`, `channel`, `text` | `text` |
| `check_kick` | `user` (who would be kicked), `source`, `channel`, `reason` | `reason` |
| `check_mode` | `user`, `channel`, `modes` (as typed, like `+k secret`) | – ; refuses the whole command |
| `check_invite` | `user` (who is invited), `source`, `name` | – ; refused without a word |
| `check_knock` | `user`, `channel`, `reason` | – |
| `check_away` | `user`, `text` | `text` |
| `check_command` | `user`, `command`, `params` (a list) | – ; not asked for PING, PONG, QUIT, OPER, REHASH and MODULE |
| `check_oper` | `user`, `name` (the oper entry), `class` | – |
| `check_whois` | `user` (who asks), `target` | return a text or a list of texts to add lines |
| `check_list` | `user`, `channel` | return `false` to leave the channel out |
| `check_names` | `user` (who asks), `member`, `channel` | return `false` to leave the member out of NAMES |

These only run for people connected to this server, like the server's own checks. Load the module on every server of the network.

### People and channels

A user is a table: `nick`, `ident`, `host`, `realname`, `account` (nil when not logged in), `server`, `ip`, `modes`, `oper`, `tls`, `service`, `away`, `here` (true when connected to this server) and `uid`.

A channel is a table: `name`, `topic`, `modes` and `count` (how many are in it).

Both are a copy of how things were at that moment. To look again, ask again.

### Commands

```lua
command("HUG", { params = 1, help = "HUG <nick>: sends a hug", opers = false }, function(user, args) ... end)
```

`params` is how many arguments are needed at least, `help` is what `/HELP HUG` shows, `opers = true` is for opers only. A name BeeIRCd already uses is refused.

### Functions

| Function | Does |
| --- | --- |
| `notice(user, text)` | A notice from the server to one person (a user table or a nick) |
| `fail(user, text)` | The same, as an error (`*** text`) |
| `numeric(user, "372", ...)` | A numeric reply, to someone on this server |
| `snotice(text)` | A server notice to the opers, with the module's name |
| `log(text)` | A line in the server log |
| `find_user(nick)` | A user table, or nil |
| `find_channel(name)` | A channel table, or nil |
| `users()`, `channels()` | Everyone, every channel |
| `members(channel)` | The people in a channel; each also has `prefix` (`@`, `+` or empty) |
| `after(seconds, function)` | Runs the function once, later |
| `every(seconds, function)` | Runs it again and again |
| `has_right(name)` | Whether the module was given a right |
| `data(user)` or `data(channel)` | A table of your own that lasts as long as that person is connected or that channel exists; gone after a reload |

Kept across restarts (in the server's database, 10 MB per module, each module its own):

| Function | Does |
| --- | --- |
| `store.set(key, value)` | Keeps a text, a number, `true` or `false`; `nil` forgets the key |
| `store.get(key)` | What was kept, or nil |
| `store.delete(key)` | Forgets it |
| `store.keys(prefix)` | The keys that start with `prefix` (all of them without one), sorted |

Acting, each with a right (see [Rights](#rights)):

| Function | Does | Right |
| --- | --- | --- |
| `kill(user, reason)` | Disconnects someone | `kill` |
| `kick(channel, user, reason)` | Kicks someone, as the server | `kick` |
| `set_topic(channel, text)` | Sets a topic, as the server | `topic` |
| `force_join(user, "#channel")` | Makes someone join | `join` |
| `set_user_mode(user, "+R")` | Sets user modes on someone connected to this server; never `o` | `usermode` |
| `ban(user or ip, reason, seconds)` | Bans the IP address from the network (an AKILL), 60 seconds to 30 days, one hour when not given | `ban` |
| `http.get(url, function(status, body, err))` | A web request; the function is called when the answer is in | `http` |
| `http.post(url, body, function(status, body, err), type)` | The same with a body; `type` is `application/json` when not given | `http` |

Web requests take at most 10 seconds and 256 KB, four at a time per module, and do not hold the server up. Addresses on the server's own machine and on private networks are refused unless the module also has the right `http-local`.

From Lua itself a module has `string`, `table`, `math` and the basic functions. It has no files, no programs and no network.

## Rights

A module may read, send notices and add commands. Anything stronger must be allowed by the admin, per module:

```toml
[module.badwords]
allow = ["kill"]
```

| Right | Lets the module |
| --- | --- |
| `kill` | disconnect people |
| `kick` | kick people from channels |
| `topic` | set channel topics |
| `join` | make people join channels |
| `usermode` | set user modes on people of this server |
| `ban` | ban an IP address from the network for a time |
| `http` | make web requests to the internet |
| `http-local` | make web requests to this machine and private networks too |

Without the right, the call fails and the opers are told which line to add.

## When a module goes wrong

- An error stops that one call. The opers get a notice with the module, the line and the message; whatever the server was doing goes on.
- A call that runs longer than `[modules] time_limit` (50 ms) is stopped the same way.
- Three errors within a minute unload the module, and the opers are told.
- A module that fails to load changes nothing.

## What is planned

Not in this version yet: changing channel modes from a module, notices to a whole channel, a module's own channel modes, user modes and extbans, services commands, admin API methods, hooks for accounts, opers, server links and network bans (`beeircd module hooks` lists them), and a limit on how much memory a module may use.
