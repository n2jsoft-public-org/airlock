# Daemon mode

With `"mode": "daemon"`, a background daemon keeps the sessions. `airlock` starts it when needed,
then shows its dashboard: closing the terminal or pressing `q` leaves the sessions running, and
`airlock` in any terminal shows them again. Several dashboards can be open at once; each has its
own selection, and closes on its own. `airlock direct <name>` opens the review step of that
database in the dashboard, so you can type the password, like in CLI mode. `airlock <type> <name>`
opens that target in the daemon, or shows its session when the daemon already runs it (same type,
name and profile, and region when `--region` is given).

```sh
airlock config set mode daemon
airlock --mode daemon    # or choose the mode for one run (--mode cli to leave it)
airlock daemon status    # running or not, version, sessions, views, socket and log
airlock daemon stop      # stop every session, then the daemon
airlock daemon restart   # after an update, or to pick up a new environment
airlock daemon run       # run the daemon in the foreground (airlock starts it this way)
```

| Key | In a daemon view |
|---|---|
| `q`, `ctrl+c` | Leave this view; the sessions keep running |
| `Q` | Stop all and quit: asks first, then stops every session and the daemon |
| `r` | After a lost connection: reconnect (starting the daemon again if needed) |

When the daemon is stopped on purpose (`Q`, `airlock daemon stop`, a signal, or the idle stop),
every view closes and prints `The airlock daemon stopped.` If the connection is lost otherwise (the
daemon crashed), the view shows a banner: `r` reconnects, `q` quits.

The daemon takes its settings from `config.json` and reads it again when it changes: the next
connections use the new values. For that reason, the setting flags `--jumphost-tag`,
`--read-only-secret-tag`, `--mcp-server-prefix` and `--mcp-clients` are refused in daemon mode: use
`airlock config set`, or run with `--mode cli`.

The gateway line of `airlock daemon status` (`gateway   on http://127.0.0.1:7821/mcp, registered in …`)
says where the gateway listens and which client files hold it, or why it is off or not registered.
When the gateway could not start (its port in use, for example), the dashboard says so too as it
connects to the daemon.
`mcp.gateway.enabled`, `mcp.gateway.port`, `mcp.serverPrefix` and `mcp.clients` apply to the gateway at the next
daemon start: run `airlock daemon restart`.

It stops on its own after `daemon.idleTimeout` (`10m`) without running session, dashboard nor call
through the gateway; ended sessions go with it. Run `airlock daemon restart` after changing it. It listens on a socket only you can open (`~/Library/Caches/airlock/daemon/daemon.sock` on
macOS, `~/.cache/airlock/daemon/daemon.sock` on Linux, or under `$XDG_CACHE_HOME` when set,
`%LocalAppData%\airlock\daemon\daemon.sock` on Windows); on macOS and Linux, airlock refuses a
daemon directory owned by another user. It logs to `daemon.log` in airlock's logs
directory, next to the MCP query logs; the previous log is kept as `daemon.log.1`. A daemon that
fails to start writes why there, and `airlock` quotes the end of it.

The IntelliJ data source and the MCP entries of a session are written in the directory of the
terminal that opened it. An AWS sign-in during an opening runs in that terminal too. A session that
drops while its terminal is gone is reconnected automatically, unless AWS asks to sign in again:
then press `r` in any dashboard.

Runs without a terminal (scripts) and `airlock exec` stay standalone, whatever the mode. `airlock update` replaces
the binary, not the running daemon: when it runs another version, the next `airlock` asks before
restarting it (restarting closes its sessions), and `airlock daemon status` tells too.
`airlock uninstall` lists the running daemon and its sessions among what it will do, and stops them
only after you confirm.
