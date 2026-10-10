# agent-serial

An MCP stdio server that lets coding agents talk to embedded devices over
serial consoles (UART, USB-serial adapters, on-board consoles) and over the
network (telnet and SSH — console servers, remote benches, VM serial taps).

agent-serial was inspired by earlier serial-over-MCP attempts that fell
short in features, security and robustness. Where those fell short,
agent-serial provides:

- **Continuous receive buffering.** A background reader per connection fills
  a ring buffer (default 1 MiB) as the device talks, so output that arrives
  between tool calls is retained instead of being lost to the OS driver
  buffer. `read` drains that buffer; overflow drops the *oldest* bytes and
  reports the count (`ring_dropped_bytes`) so the agent can detect data loss.
- **Writes never block on reads.** The port read path and write path are
  independent — an agent can send Ctrl-C while a capture window is running.
- **`status` tool.** Real connection inventory with byte counters and
  buffered-byte counts.
- **Security that is actually enforced.** Port allow/block lists and
  connection limits gate every `open`.
- **`expect` tool.** Pattern-triggered sends with a hit limit for
  time-critical reactions such as bootloader aborts.
- **Two vendored dependencies** (`go.bug.st/serial`,
  `modelcontextprotocol/go-sdk`) — no heavy runtime, no network needed to
  build.

Deliberately left out: macro automation (the agent is the loop; add later
only if you need fixed auditable procedures), runtime baud reconfiguration
(close and reopen), and hardware/software flow control (not exposed by
`go.bug.st/serial` v1; passing anything but `"none"` is a hard error).

## Build

Requires Go 1.25+. All dependencies are vendored; no network needed:

```sh
go build -mod=vendor -o agent-serial ./cmd/agent-serial
go test  -mod=vendor ./...
```

## Tools

| Tool | Purpose |
| --- | --- |
| `list_ports` | List serial ports, with USB VID:PID, product and serial number where available. |
| `open` | Open a connection and start buffering its output. Returns a `connection_id`. One connection per target. See transports below. |
| `write` | Send bytes (`encoding` utf8 default, hex, base64). **Line endings are not added** — append `"\n"`/`"\r\n"` yourself. |
| `read` | Collect device output — see semantics below. |
| `expect` | Wait for a substring, send a payload when it appears, at most `max_hits` times — see below. |
| `set_control_lines` | Set RTS and/or DTR. Toggling DTR resets many boards (Arduino, ESP32 dev boards). |
| `status` | List open connections: config, byte counters, buffered bytes, dropped bytes, errors. |
| `close` | Close a connection and release the port. |

### `read` semantics

`read` without `duration_ms` **drains**: it returns immediately with
everything buffered since the last read (possibly zero bytes,
`completion_reason: "drained"`). Because of the background reader, this is
lossy-free for normal console volumes.

`read` with `duration_ms` opens a **capture window** with these semantics:

1. Wait for the first byte, up to `initial_timeout_ms` (default: the full
   window). Silence past that ends the call with
   `completion_reason: "initial_timeout"`.
2. The first byte starts the real window: collect for at least
   `duration_ms` measured **from the first byte** (or from call start if
   data was already buffered).
3. `idle_timeout_ms` (after the most recent byte) and `max_bytes` end the
   window early, reporting `"idle_timeout"` / `"max_bytes"`. Otherwise the
   reason is `"duration_elapsed"`.

Other options: `encoding` (utf8/hex/base64), `peek` (return buffered bytes
without consuming them; immediate reads only), `max_bytes` (default 65536;
also applies to drains). Response includes `waited_ms`, `elapsed_ms`,
`dropped_bytes` (cumulative ring overflow), and chunk boundaries
(`offset`/`bytes`) for windowed reads.

UTF-8 output is lossy: invalid sequences become U+FFFD rather than failing
the whole read. Use hex/base64 for exact bytes.

Only one read may run per connection at a time; a second concurrent read is
refused immediately rather than queued or allowed to split the data.

### `expect` semantics — react to what the device says

`expect` waits for a substring (`pattern`) in the output stream and writes a
payload (`send`) each time it appears, stopping after `max_hits` matches
(default 1). It returns everything collected during the wait, with
`match_offsets` marking each hit, and a `completion_reason`:

- `"matched"` — `max_hits` reached; the last send has been written. The call
  returns **immediately** at that point, so a recurring pattern (boot loop,
  periodic banner) cannot re-trigger the send beyond `max_hits`.
- `"initial_timeout"` / `"idle_timeout"` / `"duration_elapsed"` /
  `"max_bytes"` — the pattern never (or not often enough) appeared before a
  cutoff.

Deadline scaffolding matches `read`: `duration_ms` (default 10000, measured
from the first byte), `initial_timeout_ms`, `idle_timeout_ms`, `max_bytes`.
`send_delay_ms` waits between match and send; `send_encoding` defaults to
utf8 — use hex for control bytes.

Bootloader abort (the canonical use): a U-Boot-style countdown gives you
about a second to interrupt.

```
expect(pattern="autoboot", send="03", send_encoding="hex", max_hits=1,
       duration_ms=5000, idle_timeout_ms=200)
```

The `03` (Ctrl-C) is written the moment "autoboot" appears; if the board
boot-loops and prints the banner again, `max_hits` keeps the send from
repeating. Output collected before the match (the banner itself) comes back
in `data`; the post-abort prompt stays in the ring — follow with a plain
`read`. Because `expect` drains the same continuously-buffered ring, it also
matches banners that arrived *before* the call was made.

## Server flags

```sh
agent-serial [--allow-ports ttyUSB,ttyACM] [--block-ports ttyS0]
             [--allow-hosts 10.0.0.,console-lab] [--block-hosts prod-]
             [--max-connections 16] [--ring-size 1048576]
             [--known-hosts /path/to/known_hosts]
```

Port and host lists are substring matches against the device path or
`host:port`; block wins over allow; an empty allow list means all allowed.
Connection state is per server process (stdio servers are single-client by
design).

## Transports

`open` takes a `transport` (default `serial`); everything downstream —
`read`, `expect`, the receive buffer — is transport-independent.

- **serial** — `port` is a device path (`/dev/ttyUSB0`, `COM3`). Line
  settings: `baud_rate` (required for serial), `data_bits` 5–8 default 8,
  `stop_bits` 1–2 default 1, `parity` none/odd/even default none,
  `flow_control` only `none`. `set_control_lines` works here only.
- **telnet** — `port` is `host:port` (default port 23). Protocol
  negotiation is handled internally: server option requests are refused
  (`WILL`→`DONT`, `DO`→`WONT`), subnegotiations are skipped, and `0xFF`
  bytes you send are escaped. Works with typical console servers.
  RFC 2217 com-port options are not implemented.
- **ssh** — `port` is `host:port` (default port 22) plus `username` and
  either `key_path` (preferred; `passphrase` for encrypted keys) or
  `password`. Be aware tool arguments can appear in MCP transcripts —
  prefer key files. The session requests a PTY and a shell, so remote
  consoles behave interactively; set `command` to run a specific remote
  command instead (console servers that expose a line via a command, e.g.
  `connect ttyS1`). Host keys are pinned on first use (TOFU) in the
  known-hosts file (`--known-hosts`, default
  `<user config dir>/agent-serial/known_hosts`); a key that differs from
  the pinned one is refused as a possible MITM, never silently replaced.

Set an allow list (`--allow-hosts`) when the server can reach networks you
care about: without it, an agent can open a telnet/ssh connection to any
address the server can reach.

## MCP client configuration

```json
{
  "mcpServers": {
    "serial": {
      "command": "/path/to/agent-serial",
      "args": ["--allow-ports", "ttyUSB,ttyACM"]
    }
  }
}
```

The server speaks MCP over stdio and logs only to stderr. It announces
usage instructions (workflow, line endings, `completion_reason`) via the
MCP `instructions` field.

## Hardware notes

- Console defaults: 115200 8N1, no flow control.
- Opening a port does not assert any modem line by default; use
  `set_control_lines` deliberately.
- On Linux, the user needs read/write access to the device (typically the
  `dialout` group, or a udev rule).
- USB-serial adapters survive `list_ports` being called repeatedly; ports
  can appear/disappear — re-run `list_ports` after hot-plugging.

## Layout

```
cmd/agent-serial/     server entry point and flags
internal/serial/      ring buffer, capture engine, connection + registry, encodings
internal/server/      MCP tool wiring and schemas
vendor/               go.bug.st/serial, modelcontextprotocol/go-sdk (+x/sys)
```
# agent-serial
# agent-serial
