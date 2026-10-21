# DisasterChat

A chat that needs no internet and no server. Everyone runs the same program.
Peers find each other over whatever local network exists (wifi, a phone
hotspot, an ethernet cable) and pass messages straight to one another.

Unlike a plain LAN chat, **peers also swap the messages each one missed**, so
joining late does not mean joining an empty room.

Runs in the terminal, and can serve the same chat as a web page so phones on
the network can join from a browser. Written in Go, on top of
[libp2p](https://github.com/libp2p/go-libp2p).

![DisasterChat: three peers on one network](demo.gif)

Three peers, no server and no internet. Prath and Leo find each other and talk;
Sam starts later with an empty log and still receives everything said before he
arrived.

## Try it

You need [Go](https://go.dev/dl/) 1.25.7 or newer to build it. Building also
downloads the dependencies once, so do it **while you still have internet**.
After that the binary needs nothing.

```console
go build -o disasterchat ./cmd/disasterchat
./disasterchat --nick Prath --room relief
```

On a second machine on the same network, same `--room`:

```console
./disasterchat --nick Leo --room relief
```

They find each other by themselves within a few seconds, and `peers` in the
header goes up. If it stays at 0, see [If peers don't connect](#if-peers-dont-connect).

### On one machine

Two terminals, each with **its own `--data` directory**:

```console
./disasterchat --nick Prath --room relief --data /tmp/dc1
./disasterchat --nick Leo   --room relief --data /tmp/dc2
```

Each node keeps its key in its data directory, and two nodes sharing a key
would be the same peer and never see each other. So a second copy pointed at
a directory already in use refuses to start:

```
disasterchat: another DisasterChat is already using /Users/you/.disasterchat; give this one its own, e.g. --data /tmp/dc2
```

On separate machines you don't need `--data`; each has its own
`~/.disasterchat`.

### Checking that history sync works

The interesting behaviour is what a late arrival sees. Send a few messages
between the first two, then quit one and start a third:

```console
./disasterchat --nick Sam --room relief --data /tmp/dc3
```

Sam starts with an empty log and no way to have heard any of it live, and the
whole conversation still appears.

## In a browser, and on phones

Add `--web` and the program also serves the chat as a web page. The page is
built into the binary and loads nothing from outside, so it **works with no
internet**.

```console
./disasterchat --nick Prath --room relief --web :8080
```

The links to open appear in the chat, one per network address, for example:

```
web page: http://localhost:8080/?t=tU-yfV5fF8X4
web page: http://192.168.1.20:8080/?t=tU-yfV5fF8X4
```

Open the `192.168…` one on any phone or laptop on the same wifi. That
includes iPhones, which cannot run the program itself. Use
`--web 127.0.0.1:8080` instead to allow only this computer.

The page has the same commands as the terminal, plus:

- buttons for Chat, SOS, Resource and Status, so nobody needs to type `/sos`
- filters to show only SOS calls, resources, or status updates, with counts
- SOS messages in red, and an `(1 SOS)` alert in the tab title when one arrives
- distance and direction to a message's sender (`1.2 km NE`), when you and
  they have both set `/loc`
- light and dark mode, and it reconnects by itself if the connection drops

**Whoever has the link posts as you**, under your name and key. The `?t=…`
part is a secret made fresh each time the program starts, and the page
refuses anyone without it. Share the link only with people you would hand
your keyboard to.

To try two nodes with web pages on one machine:

```console
./disasterchat --nick Prath --data /tmp/dc1 --web 127.0.0.1:8080
./disasterchat --nick Leo   --data /tmp/dc2 --web 127.0.0.1:8081
```

## Commands

The same in the terminal and on the web page.

```
/sos <text>       an emergency request
/res <text>       offer or ask for a resource (water, fuel, medicine)
/status <text>    a situation update
/nick <name>      change your display name
/loc <lat> <lon>  tag your messages with coordinates ( /loc clear to stop )
/peers            who you are connected to
/me               your id and address, to give to someone else
/dial <address>   connect by hand, if this network blocks discovery
/quit             exit
```

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--nick` | your computer's name | Display name |
| `--room` | `chat-room` | Room to join; must match on every device |
| `--network` | `disasterchat` | Discovery tag; must match on every device |
| `--port` | `0` | Port to listen on; 0 picks a free one |
| `--data` | `~/.disasterchat` | Where your key and messages are kept; one per running copy |
| `--peer` | none | Address of a peer to connect to; may be repeated |
| `--web` | off | Also serve a web page here: `:8080` for the network, `127.0.0.1:8080` for this computer |

## If peers don't connect

`peers` stays at 0, or messages typed in one place never show up in another.

- **Same machine, two copies?** Each needs its own `--data`, as above.
- **Same `--room` and `--network` everywhere?** They must match exactly.
- **macOS:** the first run asks for permission to find devices on the local
  network. If that was missed or denied, discovery silently never works.
  Allow it under *System Settings → Privacy & Security → Local Network*.
- **Guest wifi, office networks, some VPNs and hotspots** block the
  broadcast that discovery uses. Connect by hand instead: run `/me` on one
  machine, and on the other run `/dial` with one of the addresses it printed
  (pick the one on the shared network, like `/ip4/192.168.…`). Or pass it at
  startup with `--peer`.
- **Firewall:** allow incoming connections for `disasterchat` if your system
  asks.

Other errors you might see:

| Message | Meaning |
|---|---|
| `another DisasterChat is already using …` | Two copies on one `--data`; give one its own |
| `--web "…" is not an address` | Use `:8080` or `127.0.0.1:8080`, with the colon |
| `starting web page: … address already in use` | Something else has that port; pick another |
| `saved, but could not broadcast yet` | Nobody in range; the message goes out at the next sync |

Details go to `<data>/disasterchat.log`, never to the screen.

## What it does and doesn't protect

Messages are **signed, not encrypted**. Nobody can forge or change a message
in your name, even when it is passed along by others. But anyone on the
network who knows the room name can read everything, nicknames are not
verified (two people can both be "Leo"), and there is no protection against
someone flooding the room. Don't send anything you would not say out loud in
the shelter. [NOTES.md §8](NOTES.md) has the full threat model.

## The code

```
cmd/disasterchat/     the program you run: flags, startup, wiring
internal/chat/        one peer: messages, history, discovery, sync
internal/command/     the slash commands, shared by both interfaces
internal/ui/          the terminal interface
internal/web/         the web page, built into the binary
```

`chat` does the networking, `ui` and `web` are two ways of showing it, and
`command` is what both understand when you type. `internal/` means nothing
outside this project can import them, which is the normal way to say "these
are implementation details, not a library".

```console
go test ./... -race
```

`TestDiscovery` needs a network that allows multicast, so it fails on most
CI runners and behind some VPNs. That is the network, not the code.

### How it fits together

```
   you type                                            the network
      |                                                     |
 [ ui / web ] --command--> [ chat.Node ] --broadcast--> other peers
      ^                          |                          |
      |                          v                          |
      +---- Incoming ------ [ History ] <---- sync.go <-----+
                           (saved to disk)
```

`chat.Node` hands out two channels: `Incoming` for new messages, your own
included, and `Notices` for status lines. `main.go` passes whatever arrives
on them to the terminal and, with `--web`, to every open web page. That is
the whole interface between the network and the screens.

**Finding people.** mDNS shouts "anyone there?" on the local network. Anyone
running DisasterChat with the same `--network` tag answers, and we connect.

**Sending.** Messages go out over GossipSub, libp2p's broadcast. Every peer in
the room gets a copy and passes it along, so two people can talk through a
third even if they cannot reach each other directly.

**Remembering.** Every message you send or receive is appended to a plain
text file, one JSON object per line. There's no database: you can read it
with `cat`.

**Catching up.** This is the part worth understanding, in `sync.go`. GossipSub
only delivers to whoever is listening at the exact moment you press enter. In a
disaster that loses nearly everything, because devices keep wandering out of
range or running flat. So whenever two nodes meet, each says what it already
has and they fill in each other's gaps.

**Signing.** Every message is signed with your key, and checked on arrival.
This matters because of the previous point: when a message is passed along by
someone else, the signature is what stops them changing it. An ed25519 peer ID
contains its own public key, so this needs no server and no key exchange,
which is what makes it work offline at all.

### What's on disk

Everything lives in `--data` (default `~/.disasterchat`):

```
identity.key         your key, made on first run; keeping it keeps your id
rooms/<room>.jsonl   every message in that room, one per line
disasterchat.log     diagnostics, kept off the screen
lock                 held while running, so a second copy refuses
```

Deleting `identity.key` gives you a new identity. Deleting a room file only
forgets your copy; peers will sync it back to you.

For why it is built this way, and what will bite you if you change it, read
[NOTES.md](NOTES.md).
