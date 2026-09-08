// Package chat is one DisasterChat peer: it finds others on the local
// network, exchanges messages, and keeps everything it has seen.
package chat

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	ma "github.com/multiformats/go-multiaddr"
)

type Options struct {
	Port    string   // "0" picks a free one
	Room    string   // must match across peers
	Nick    string   // your display name
	Network string   // mDNS tag; must match across peers
	DataDir string   // where the key and message log live
	Dial    []string // peers to connect to by hand
}

type Node struct {
	ctx   context.Context
	host  host.Host
	self  peer.ID
	key   crypto.PrivKey
	room  string
	topic *pubsub.Topic
	sub   *pubsub.Subscription
	log   *History

	mu       sync.Mutex
	nick     string
	lat, lon *float64
	nicks    map[peer.ID]string // last name we saw each peer use

	Incoming chan *Message // messages we have not seen before
	Notices  chan string   // status lines for the UI
}

// Start brings a node up: key, host, discovery, room, history.
func Start(ctx context.Context, opts Options) (*Node, error) {
	key, err := loadKey(filepath.Join(opts.DataDir, "identity.key"))
	if err != nil {
		return nil, err
	}

	h, err := libp2p.New(
		libp2p.Identity(key),
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/"+opts.Port,
			"/ip4/0.0.0.0/udp/"+opts.Port+"/quic-v1",
		),
	)
	if err != nil {
		return nil, fmt.Errorf("starting host: %w", err)
	}

	log, err := OpenHistory(filepath.Join(opts.DataDir, "rooms", opts.Room+".jsonl"))
	if err != nil {
		return nil, err
	}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, fmt.Errorf("starting pubsub: %w", err)
	}
	topic, err := ps.Join("disasterchat/" + opts.Room)
	if err != nil {
		return nil, fmt.Errorf("joining room: %w", err)
	}
	sub, err := topic.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("subscribing to room: %w", err)
	}

	n := &Node{
		ctx: ctx, host: h, self: h.ID(), key: key,
		room: opts.Room, topic: topic, sub: sub, log: log,
		nick:     opts.Nick,
		nicks:    map[peer.ID]string{},
		Incoming: make(chan *Message, 128),
		Notices:  make(chan string, 128),
	}

	h.SetStreamHandler(syncProtocol, n.serveSync)
	if err := n.discover(opts.Network); err != nil {
		return nil, err
	}
	for _, addr := range opts.Dial {
		go func(a string) {
			if err := n.Dial(a); err != nil {
				n.notify(err.Error())
			}
		}(addr)
	}

	go n.readLoop()
	go n.syncLoop()
	return n, nil
}

// loadKey reads the node's key, making one on first run.
func loadKey(path string) (crypto.PrivKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		return crypto.UnmarshalPrivateKey(raw)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	raw, err := crypto.MarshalPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return key, os.WriteFile(path, raw, 0o600)
}

// found receives peers spotted by mDNS.
type found struct{ peers chan peer.AddrInfo }

func (f *found) HandlePeerFound(p peer.AddrInfo) {
	select {
	case f.peers <- p:
	default:
	}
}

// discover announces us over mDNS and connects to whoever answers.
func (n *Node) discover(tag string) error {
	f := &found{peers: make(chan peer.AddrInfo, 32)}
	if err := mdns.NewMdnsService(n.host, tag, f).Start(); err != nil {
		return fmt.Errorf("starting mdns: %w", err)
	}

	go func() {
		for {
			select {
			case <-n.ctx.Done():
				return
			case p := <-f.peers:
				if p.ID == n.self || n.host.Network().Connectedness(p.ID) == network.Connected {
					continue
				}
				ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
				err := n.host.Connect(ctx, p)
				cancel()
				if err != nil {
					continue
				}
				n.notify("connected to " + n.name(p.ID))
				go n.syncWith(p.ID)
			}
		}
	}()
	return nil
}

func (n *Node) Dial(addr string) error {
	a, err := ma.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("bad address %q: %w", addr, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(a)
	if err != nil {
		return fmt.Errorf("bad address %q: %w", addr, err)
	}
	ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
	defer cancel()
	if err := n.host.Connect(ctx, *info); err != nil {
		return fmt.Errorf("could not reach %s: %w", addr, err)
	}
	n.notify("connected to " + n.name(info.ID))
	go n.syncWith(info.ID)
	return nil
}

// Send signs a message, saves it, and broadcasts it.
func (n *Node) Send(kind Kind, body string) (*Message, error) {
	lat, lon := n.Location()
	m, err := NewMessage(n.key, n.self, n.room, n.Nick(), kind, body, lat, lon)
	if err != nil {
		return nil, err
	}
	if _, err := n.log.Add(m); err != nil {
		return nil, err
	}

	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := n.topic.Publish(n.ctx, raw); err != nil {
		return m, fmt.Errorf("saved, but could not broadcast yet: %w", err)
	}
	return m, nil
}

// readLoop stores the messages that arrive on the room.
func (n *Node) readLoop() {
	for {
		got, err := n.sub.Next(n.ctx)
		if err != nil {
			return
		}
		var m Message
		if json.Unmarshal(got.Data, &m) != nil || m.Room != n.room {
			continue
		}
		isNew, err := n.log.Add(&m)
		if err != nil {
			n.notify("ignored a bad message: " + err.Error())
			continue
		}
		if isNew {
			n.remember(&m)
			n.deliver(&m)
		}
	}
}

func (n *Node) deliver(m *Message) {
	select {
	case n.Incoming <- m:
	default:
	}
}

func (n *Node) notify(text string) {
	select {
	case n.Notices <- text:
	default:
	}
}

// remember notes what name a peer is going by.
func (n *Node) remember(m *Message) {
	if id, err := peer.Decode(m.Author); err == nil && m.Nick != "" {
		n.mu.Lock()
		n.nicks[id] = m.Nick
		n.mu.Unlock()
	}
}

// name is a peer's nickname if we have heard one, else a short id.
func (n *Node) name(id peer.ID) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if nick, ok := n.nicks[id]; ok {
		return nick
	}
	return id.ShortString()
}

func (n *Node) Peers() []string {
	var out []string
	for _, id := range n.host.Network().Peers() {
		out = append(out, n.name(id))
	}
	return out
}

func (n *Node) PeerCount() int    { return len(n.host.Network().Peers()) }
func (n *Node) Room() string      { return n.room }
func (n *Node) Self() peer.ID     { return n.self }
func (n *Node) History() *History { return n.log }

func (n *Node) Nick() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.nick
}

func (n *Node) SetNick(s string) {
	n.mu.Lock()
	n.nick = s
	n.mu.Unlock()
}

// SetLocation tags later messages with coordinates. nil clears it.
func (n *Node) SetLocation(lat, lon *float64) {
	n.mu.Lock()
	n.lat, n.lon = lat, lon
	n.mu.Unlock()
}

func (n *Node) Location() (*float64, *float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lat, n.lon
}

// Addrs are the addresses someone else can use with /dial.
func (n *Node) Addrs() []string {
	var out []string
	for _, a := range n.host.Addrs() {
		out = append(out, a.String()+"/p2p/"+n.self.String())
	}
	return out
}

func (n *Node) Close() error {
	n.log.Close()
	return n.host.Close()
}
