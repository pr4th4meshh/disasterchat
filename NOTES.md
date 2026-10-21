# NOTES

Everything worth knowing about how DisasterChat works, why it is built this
way, and what will bite you if you change it. The code is kept deliberately
free of long explanatory comments; the explanations live here.

---

## 1. The mental model

One binary. Every person runs the same one. There is no server, no bootstrap
node, no account, and no internet.

```
   you type                                          the network
      |                                                   |
   [ ui ] --Send--> [ chat.Node ] --broadcast--> other peers
      ^                   |                               |
      |                   v                               |
      +-- Incoming --- [ History ] <---- sync.go <--------+
                      (saved to disk)
```

`chat.Node` exposes exactly two channels to the outside world — `Incoming`
for new messages and `Notices` for status lines. `main.go` pumps both into
bubbletea. That is the entire interface between the network and the screen.

Four files do the work, in the order worth reading them:

| File | What it does |
|---|---|
| `internal/chat/message.go` | what a message is; signing and verifying |
| `internal/chat/history.go` | the append-only message log on disk |
| `internal/chat/sync.go` | how two peers fill each other's gaps |
| `internal/chat/node.go` | the network: discovery, sending, receiving |
| `internal/command/command.go` | slash commands, shared by terminal and web |
| `internal/ui/ui.go` | the terminal interface |
| `internal/web/web.go` | the web page: events stream, send, token |
| `cmd/disasterchat/main.go` | flags, startup, wiring |

---

## 2. Messages

A `Message` is a flat JSON struct. Three fields deserve attention:

- **`ID`** — the SHA-256 of the message with `ID` and `Sig` blanked out, hex
  encoded. It is content-derived, so the same message computed on two
  machines yields the same ID. This is what makes deduplication work without
  any coordination.
- **`Author`** — the sender's libp2p peer ID as a string.
- **`Sig`** — an ed25519 signature over those same blanked bytes.

### Why signing matters here specifically

In a plain LAN chat you could skip signatures: you received the bytes
directly from the sender. DisasterChat cannot, because **messages are relayed
by third parties** during history sync. Anyone who backfills you a message
had the opportunity to edit it first. The signature is what makes a relayed
message as trustworthy as a direct one.

The elegant part: an ed25519 libp2p peer ID *contains its own public key*.
`peer.Decode(author).ExtractPublicKey()` needs no server, no key exchange, no
directory. That property is the reason this design works fully offline at
all.

`Verify()` checks three things in order: the body is non-empty, the ID
actually matches the hash of the contents, and the signature is valid for the
author's key.

### ⚠️ The determinism trap

`signedBytes()` marshals the struct with `encoding/json`, which writes fields
**in struct declaration order**. Both sides must produce byte-identical
output for the same message.

**Reordering, renaming, or retyping any field in `Message` silently breaks
signature compatibility with every previously built binary and every message
already on disk.** Old messages will fail `Verify()` and be dropped on load.
If you must change the struct, treat it as a wire-format break and bump the
sync protocol version.

---

## 3. History

`History` is every message this node has ever seen, appended to a plain text
file — one JSON object per line — at `<data>/rooms/<room>.jsonl`.

No database, on purpose. You can read it with `cat` on a half-broken laptop,
recover it with `grep`, and there is nothing to install or migrate. For the
scale this tool targets (a neighbourhood, a shelter, a valley) a text file is
the right answer.

Things to know:

- **A corrupt or unverifiable line is skipped, not fatal.** A truncated write
  from a machine that lost power must not stop the program from starting.
- **`Add()` is the single funnel.** Messages you type, messages that arrive
  live over pubsub, and messages backfilled by a peer all go through it. It
  verifies, dedupes by ID, appends to disk, and reports whether the message
  was new. That is why a message arriving twice by different routes is stored
  and displayed exactly once.
- **`keep()` re-sorts the whole slice on every insert.** O(n log n) per
  message. Fine at disaster scale, wrong at 100k messages — an insertion into
  a sorted slice is the fix if that ever matters.
- The line scanner is capped at **4 MB per line**.
- Callers of `keep()` must already hold the mutex.

---

## 4. Sync — the part that makes this more than a LAN chat

**This is the most important file in the project.**

GossipSub is live-only. It delivers to whoever is listening at the instant
you press enter, and to nobody else, ever. In a disaster that property loses
almost everything, because devices are constantly wandering out of range,
going flat, or being switched on late.

So: whenever two nodes meet, each says what it already has, and they fill in
each other's gaps. A message written alone in a valley reaches town on the
next person who walks past.

### The protocol

Protocol ID `/disasterchat/sync/1.0.0`, three JSON messages over one stream:

```
caller                                                    answerer
  |                                                          |
  |-- hello{room, have:[ids I hold]} ----------------------->|
  |                                                          |
  |<-- reply{send:[msgs you lack], want:[ids I lack]} -------|
  |                                                          |
  |-- push{send:[the msgs you asked for]} ------------------>|
  |                                                          |
```

One round trip, fully symmetric — both sides end up complete. IDs are cheap
to list (64 hex chars each) and only actual gaps move as full messages.

### When it runs

- Immediately on connecting to a newly discovered peer (`discover`)
- Immediately after a manual `/dial`
- Every **30 seconds** with every connected peer, to catch anything missed
  during a brief partition

Both sides enforce a **20-second** deadline and a matching `room`. Every
error path just returns — a peer that vanished mid-sync is normal, not
exceptional, and the next tick will retry.

`store()` runs incoming messages through `History.Add`, so a relaying peer
still cannot alter what a message says: only the original author's key
produces a valid signature.

---

## 5. The node and the network

### Listening on `0.0.0.0`, not `127.0.0.1`

This is the single most consequential line in the project. Binding to
loopback means the address you advertise over mDNS tells other devices to
connect *to themselves* — so the app appears to work between two terminals on
one laptop and never works between two actual machines. It listens on all
interfaces, over **both TCP and QUIC** (QUIC copes better with flaky wifi).

### Discovery

mDNS multicasts "anyone there?" on the local network under the `--network`
tag. Anyone running DisasterChat with the same tag answers, and we connect
and immediately sync.

Discovery repeats itself constantly, so `HandlePeerFound` drops duplicates
into a buffered channel and ignores overflow. Already-connected peers and our
own ID are filtered out.

mDNS requires multicast, which guest wifi, corporate networks, and some VPNs
block outright. `/dial` with an address from someone else's `/me` is the
manual fallback and always works if the two machines can reach each other.

### Broadcast

GossipSub, on topic `"disasterchat/" + room`. Every peer in the room relays,
so two people who cannot reach each other directly can still talk through a
third.

### Send order: save first, then publish

`Send()` writes to history *before* it publishes. Writing a message with
nobody in range therefore does not lose it — it is on disk, and it goes out
at the next sync whenever somebody appears. A publish failure is reported to
the user but is not treated as losing the message.

### Channel backpressure

`Incoming` and `Notices` are buffered at 128 and **dropped when full**
(`deliver`/`notify` use a `default:` case). A slow or stalled UI must never
stall the network loop. The consequence: under extreme load the UI can miss a
*notice*. It cannot miss a *message*, because history on disk is the
authoritative record and the UI reads from it.

### Identity

An ed25519 key at `<data>/identity.key`, mode `0600`, generated on first run.
Keeping it means your peer ID is stable across restarts, so the same device
is provably the same participant tomorrow.

**Two nodes must never share a data directory** — they would be the same
peer, find only themselves over mDNS, and never connect. `Start()` enforces
this with an exclusive `flock` on `<data>/lock`, which the OS releases when
the process exits, crash included. Not enforced on Windows yet. This is why running two instances on one machine needs separate
`--data` paths.

---

## 6. The UI

A bubbletea program. The shape: `Update` handles one event and returns new
state, `View` renders that state, nothing else touches either.

- Backfilled messages can be **older than what is already on screen**, so
  the transcript re-sorts by timestamp on every insert rather than appending.
- The viewport follows the bottom only if the user was already at the bottom,
  so scrolling up to read is not yanked away by an arriving message.
- A one-second tick refreshes the peer and message counters in the header.
- All libp2p and stdlib logging is redirected to `<data>/disasterchat.log`.
  Anything written to stdout would tear the interface apart.

---

### The web page

`--web <addr>` serves the same node to a browser, alongside the terminal.

- **One page, two endpoints.** `GET /events` is a server-sent event stream:
  a `state` event, every message in history, `ready`, then live `msg`,
  `notice`, and a `state` every two seconds. `POST /send` takes `{"text":…}`
  and runs it through `command.Run`, exactly like a line typed in the
  terminal, returning the notes for that browser alone.
- **Server-sent events, not WebSockets**, so it needs nothing outside the
  standard library. The page is `index.html`, embedded with `go:embed`, and
  loads nothing from outside — it must keep working with no internet.
- **A reconnecting browser simply gets the whole history again** and drops
  the ids it already has. That is also how a slow browser is handled: if its
  buffer fills, it is disconnected rather than allowed to stall anyone, and
  catches up when it reconnects.
- **Your own messages are on `Incoming` too.** `Send()` delivers to
  `Incoming` as well as publishing, so a line typed in the browser appears in
  the terminal and vice versa. Both interfaces dedupe by id.
- **The token is the only access control.** Nine random bytes, made fresh
  each run, required on both endpoints (header `X-Token`, or `?t=` for the
  event stream, since `EventSource` cannot set headers). Anyone with the link
  acts as this node: same key, same nick, `/nick` and `/loc` included. There
  is no per-browser identity. Plain HTTP, so on shared wifi the token can be
  sniffed — the same exposure as the messages themselves (§8).

## 7. Constants you should not change casually

Changing any of these breaks compatibility with already-built binaries. Peers
on different values will connect and then silently fail to exchange anything.

| Value | Where | Consequence |
|---|---|---|
| `/disasterchat/sync/1.0.0` | `sync.go` | sync stops working entirely |
| `"disasterchat/" + room` | `node.go` | pubsub messages never arrive |
| field order in `Message` | `message.go` | every signature fails |
| default `--network` tag | `main.go` | peers stop discovering each other |

---

## 8. Threat model

Be honest about this, because the project invites more trust than it earns.

**What it gives you**

- Messages are authenticated. A relayed message cannot be forged or altered
  without the author's private key.
- Identities are stable and self-certifying, with no server involved.

**What it does not give you**

- **No confidentiality.** Messages are signed, not encrypted. Anyone on the
  network who knows the room name reads everything. There is no end-to-end
  encryption and no private messaging.
- **The room name is the only access control**, and it travels in the clear.
- **No spam or flood protection.** Any participant can fill your disk.
- **Pseudonymous, not anonymous.** Your peer ID is stable by design, so all
  your messages are linkable across sessions and your key file identifies
  your device.
- **Nicknames are not authenticated.** `Nick` is free text inside a signed
  message: it is bound to a key, but two people can pick the same name. The
  peer ID is the real identity.
- **LAN only.** mDNS does not cross routers. No Bluetooth, no LoRa, no relay
  over the internet.

---

## 9. Tests

```console
go test ./... -race
```

- **`TestLateJoinerGetsHistory`** — the one that matters. A third node starts
  *after* a message was sent and still ends up with it. Only `sync.go` can
  make this pass; GossipSub alone never delivers to someone who was absent.
- **`TestEditedMessageIsRejected`** — tampering is caught by `Verify`.
- **`TestEditingTheFileOnDiskDropsTheMessage`** — same guarantee, applied to
  the on-disk log.
- **`TestDiscovery`** — mDNS actually finds a peer. This one **depends on the
  machine and network**: it fails where multicast is blocked, which includes
  most CI runners. Gate it before relying on a green CI badge. If it fails
  locally, discovery is blocked on your network — use `/dial`.

---

## 10. Known rough edges

- History re-sorts on every insert (§3).
- No message expiry or log rotation; the log grows without bound.
- Sync sends the *complete* ID list every time. Fine for thousands of
  messages, wasteful for millions.
- No compaction of the on-disk log after corrupt lines are skipped.
- macOS asks for local-network permission on first run; if the prompt is
  missed, discovery silently never works until it is granted under
  *System Settings → Privacy & Security → Local Network*.
