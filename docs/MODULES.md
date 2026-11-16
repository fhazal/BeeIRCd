# Writing modules

A module adds something of your own to BeeIRCd: a greeting, a command, a word filter. It is one Lua file. You don't need a compiler, and you load it while the server runs.

This is the first version of modules. It has the hooks and functions listed here; more are planned (see [What is planned](#what-is-planned)).

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

Asked before it happens:

| Hook | `e` has | The function may |
| --- | --- | --- |
| `check_message` | `user`, `channel` or `target`, `text`, `notice` | return a text to refuse it with that reason; return `false` to drop it without a word; set `e.text` to change it; return nothing to let it pass |

`check_message` only runs for people connected to this server, like the server's own checks. Load the module on every server of the network.

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
| `kill(user, reason)` | Disconnects someone. Needs the right `kill` |

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

Without the right, the call fails and the opers are told which line to add.

## When a module goes wrong

- An error stops that one call. The opers get a notice with the module, the line and the message; whatever the server was doing goes on.
- A call that runs longer than `[modules] time_limit` (50 ms) is stopped the same way.
- Three errors within a minute unload the module, and the opers are told.
- A module that fails to load changes nothing.

## What is planned

Not in this version yet: the other "asked before" hooks (`check_join`, `check_nick`, `check_connect` and more; `beeircd module hooks` lists them), functions that act on channels (modes, kicks, topics, bans), data that is kept across restarts, web requests, a module's own channel modes, user modes and extbans, services commands and admin API methods.
