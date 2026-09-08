# DisasterChat

A chat that needs no internet and no server. Everyone runs the same program.
Peers find each other over whatever local network exists — wifi, a phone
hotspot, an ethernet cable — and pass messages straight to one another.

Unlike a plain LAN chat, **peers also swap the messages each one missed**, so
joining late does not mean joining an empty room.

Terminal only. Written in Go, on top of [libp2p](https://github.com/libp2p/go-libp2p).

![DisasterChat: three peers on one network](demo.gif)

Three peers, no server and no internet. Prath and Leo find each other and talk;
Sam starts later with an empty log and still receives everything said before he
arrived.

## Try it

```console
go build -o disasterchat ./cmd/disasterchat
./disasterchat --nick Prath --room relief
```

On a second machine on the same network, same `--room`:

```console
./disasterchat --nick Leo --room relief
```

They find each other by themselves. If they don't, see *If discovery doesn't
work* below.

### On one machine

Two terminals, two data directories, and they will find each other the same
way:

```console
./disasterchat --nick Prath --room relief --data /tmp/dc1
./disasterchat --nick Leo   --room relief --data /tmp/dc2
```

```
--data is only needed when you run more than one node on the same machine.
```

Separate `--data` matters: each node keeps its own key there, and two nodes
sharing one key would be the same peer.

### Checking that history sync works

The interesting behaviour is what a late arrival sees. Send a few messages
between the first two, then quit one and start a third:

```console
./disasterchat --nick Sam --room relief --data /tmp/dc3
```

Sam starts with an empty log and no way to have heard any of it live, and the
whole conversation still appears.

### Commands

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

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--nick` | your computer's name | Display name |
| `--room` | `chat-room` | Room to join; must match on every device |
| `--network` | `disasterchat` | Discovery tag; must match on every device |
| `--port` | `0` | Port to listen on; 0 picks a free one |
| `--data` | `~/.disasterchat` | Where your key and messages are kept |
| `--peer` | — | Address of a peer to connect to; may be repeated |

## The code

```
cmd/disasterchat/     the program you run: flags, startup, wiring
internal/chat/        one peer: messages, history, discovery, sync
internal/ui/          the terminal interface
```

Two packages doing the work, and a thin `main` joining them. `internal/` means
nothing outside this project can import them, which is the normal way to say
"these are implementation details, not a library".

Read `internal/chat` in this order:

### How it fits together

```
   you type                                          the network
      |                                                   |
   [ ui ] --Send--> [ chat.Node ] --broadcast--> other peers
      ^                   |                               |
      |                   v                               |
      +-- Incoming --- [ History ] <---- sync.go <--------+
                      (saved to disk)
```

`chat.Node` hands the UI two channels — `Incoming` for new messages and
`Notices` for status lines — and `main.go` passes whatever arrives on them to
bubbletea. That is the whole interface between the two packages.

**Finding people.** mDNS shouts "anyone there?" on the local network. Anyone
running DisasterChat with the same `--network` tag answers, and we connect.

**Sending.** Messages go out over GossipSub, libp2p's broadcast. Every peer in
the room gets a copy and passes it along, so two people can talk through a
third even if they cannot reach each other directly.

**Remembering.** Every message you send or receive is appended to a plain text
file, one JSON object per line. No database — you can read it with `cat`.

**Catching up.** This is the part worth understanding, in `sync.go`. GossipSub
only delivers to whoever is listening at the exact moment you press enter. In a
disaster that loses nearly everything, because devices keep wandering out of
range or running flat. So whenever two nodes meet, each says what it already
has and they fill in each other's gaps.

**Signing.** Every message is signed with your key, and checked on arrival.
This matters because of the previous point: when a message is passed along by
someone else, the signature is what stops them changing it. An ed25519 peer ID
contains its own public key, so this needs no server and no key exchange —
which is what makes it work offline at all.

**Your key** lives in `~/.disasterchat/identity.key` and is made on first run.
Keeping it means your id stays the same between restarts.