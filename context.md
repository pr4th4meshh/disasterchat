# Context

Orientation for anyone — human or agent — picking up this codebase. Answers
"where does X live, what must not break, how do I check it still works".

- **[README.md](README.md)** — what it is and how to run it.
- **[NOTES.md](NOTES.md)** — why it is built this way, in depth.
- This file — the map and the rules.

---

## What it is

`disasterchat` is a single Go binary: a serverless, internet-free chat for a
local network. Peers find each other over mDNS, broadcast over GossipSub, and
— the part that distinguishes it from a LAN chat — **sync missed messages
whenever two of them meet**, so a late joiner sees the whole conversation.

Module `github.com/pr4th4meshh/disasterchat`, Go 1.25.7. ~1,350 lines of Go
across six files. Terminal-only (bubbletea). No server, no database, no
account, no network calls off the LAN.

## Layout

| Path | Lines | Role |
|---|---|---|
| `cmd/disasterchat/main.go` | 104 | flags, log redirection, signal handling, wiring |
| `internal/chat/message.go` | 106 | the `Message` struct; signing and `Verify()` |
| `internal/chat/history.go` | 153 | append-only JSONL log, dedup, set queries |
| `internal/chat/sync.go` | 107 | the gap-filling protocol between two peers |
| `internal/chat/node.go` | 327 | host, identity, mDNS, pubsub, public API |
| `internal/command/command.go` | 135 | slash commands, shared by terminal and web |
| `internal/ui/ui.go` | 232 | bubbletea model, rendering |
| `internal/web/web.go` | 280 | `--web`: embedded page, SSE stream, send, token |
| `internal/web/index.html` | — | the page itself; no external requests |
| `internal/chat/chat_test.go` | 219 | four integration tests over real nodes |

Read `internal/chat` in that order — `message` → `history` → `sync` → `node`
— each builds on the one before.

## The one diagram

```
   you type                                          the network
      |                                                   |
   [ ui ] --Send--> [ chat.Node ] --broadcast--> other peers
      ^                   |                               |
      |                   v                               |
      +-- Incoming --- [ History ] <---- sync.go <--------+
                      (saved to disk)
```

`chat.Node` exposes exactly two channels outward: `Incoming` (*Message) and
`Notices` (string). `main.go` pumps both into bubbletea via `prog.Send`. That
is the entire seam between network and screen — the UI never touches libp2p,
and `chat` never imports `ui`.

## Data on disk

Everything lives under `--data` (default `~/.disasterchat`):

```
identity.key                 ed25519 private key, 0600, made on first run
lock                         held while a node runs, so a second one refuses
rooms/<room>.jsonl           the message log, one JSON object per line
disasterchat.log             libp2p + stdlib logging, kept off the screen
```

All three are gitignored, along with the built `disasterchat` binary.

**Two processes must never share a `--data` directory** — they would load the
same key and be the same peer. `lockDataDir` (unix only) makes the second
one refuse to start. Running two instances locally needs two paths.

## Invariants — break these and it silently stops interoperating

| Invariant | Where | What breaks |
|---|---|---|
| Field order in `Message` | `message.go` | `signedBytes()` marshals in declaration order; every existing signature and every message on disk fails `Verify()` |
| `/disasterchat/sync/1.0.0` | `sync.go` | history sync stops entirely; live chat still works, hiding the fault |
| Topic `"disasterchat/" + room` | `node.go` | pubsub messages never arrive |
| Default `--network` tag | `main.go` | peers stop discovering each other |
| Listen on `0.0.0.0`, not loopback | `node.go` | works between two terminals, never between two machines |
| `History.Add` is the only write path | `history.go` | dedup and verification are bypassed |

Each of these fails *quietly*: the app runs, peers connect, and nothing
arrives. Treat a change to any of them as a wire-format break.

## Behaviours that look like bugs but are deliberate

- **`Send()` writes to history before publishing.** A message typed with
  nobody in range is not lost; it ships at the next sync.
- **`Incoming`/`Notices` are buffered at 128 and dropped when full.** A stalled
  UI must never stall the network. A *notice* can be lost; a *message* cannot,
  because disk is the authoritative record.
- **Corrupt or unverifiable log lines are skipped, not fatal.** A truncated
  write from a machine that lost power must not stop startup.
- **`History.keep()` re-sorts the whole slice per insert**, and the UI re-sorts
  its transcript per insert. Backfilled messages are older than what is
  already on screen, so appending would order them wrong. O(n log n) per
  message is fine at disaster scale.
- **Every sync error path just returns.** A peer that vanished mid-sync is
  normal; the 30-second tick retries.

## The sync protocol in six lines

Three JSON values over one stream, one round trip, symmetric:

```
caller  --hello{room, have:[my ids]}-->              answerer
        <--reply{send:[msgs you lack], want:[ids I lack]}--
        --push{send:[the msgs you asked for]}-->
```

Runs on discovery, after a manual `/dial`, and every 30s with every connected
peer. 20-second deadline both sides; mismatched `room` is dropped.

## Security posture

Signed, **not encrypted**. Messages are authenticated end-to-end — an
ed25519 peer ID contains its own public key, so a relaying peer cannot alter
what it passes on, with no server or key exchange. But there is no
confidentiality, the room name is the only access control and travels in the
clear, nicknames are unauthenticated free text, peer IDs are stable and
therefore linkable, and there is no flood protection. NOTES §8 states this in
full; don't let a change imply more than this.

## Working on it

```console
go build -o disasterchat ./cmd/disasterchat
go test ./... -race
go vet ./...
```

`TestLateJoinerGetsHistory` is the test that matters — only `sync.go` can make
it pass. `TestDiscovery` exercises real mDNS and **fails wherever multicast is
blocked**, including most CI runners and behind some VPNs; a failure there is
usually the network, not the code. Tests give themselves random room and
discovery tags so they cannot see each other's traffic, or a real
DisasterChat on the same LAN.

To exercise the real behaviour by hand, two terminals:

```console
./disasterchat --nick Prath --room relief --data /tmp/dc1
./disasterchat --nick Leo   --room relief --data /tmp/dc2
```

Send a few messages, then start a third with a fresh `--data` and watch the
backlog arrive.

## House style

The code carries short comments and no long explanations — the reasoning
lives in NOTES.md, and that split is intentional. Comments are lowercase
sentences explaining *why*, not restating the call. Errors are wrapped with
`fmt.Errorf(... %w)` in plain language aimed at someone in a bad situation
("could not reach %s", "saved, but could not broadcast yet"). Exported
identifiers in `chat` are the UI's whole vocabulary; keep that surface small.

## Known rough edges

- The log grows without bound — no expiry, rotation, or compaction after
  corrupt lines are skipped.
- Sync sends the complete ID list every time: fine for thousands of messages,
  wasteful for millions.
- LAN only. mDNS does not cross routers; no Bluetooth, LoRa, or internet relay.
- macOS prompts for local-network permission on first run; if that prompt is
  missed, discovery silently never works until it is granted under
  *System Settings → Privacy & Security → Local Network*.
