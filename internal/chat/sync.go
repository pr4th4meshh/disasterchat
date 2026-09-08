package chat

import (
	"context"
	"encoding/json"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const syncProtocol = protocol.ID("/disasterchat/sync/1.0.0")

// The three things sent over a sync connection, in order:
type hello struct {
	Room string   `json:"room"`
	Have []string `json:"have"` // ids the caller already holds
}
type reply struct {
	Send []*Message `json:"send"` // messages the caller was missing
	Want []string   `json:"want"` // ids the answerer was missing
}
type push struct {
	Send []*Message `json:"send"` // the messages that were wanted
}

const syncTimeout = 20 * time.Second

// syncWith is the calling side of a sync.
func (n *Node) syncWith(id peer.ID) {
	ctx, cancel := context.WithTimeout(n.ctx, syncTimeout)
	defer cancel()

	stream, err := n.host.NewStream(ctx, id, syncProtocol)
	if err != nil {
		return
	}
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(syncTimeout))

	out := json.NewEncoder(stream)
	in := json.NewDecoder(stream)

	if out.Encode(hello{Room: n.room, Have: n.log.IDs()}) != nil {
		return
	}
	var r reply
	if in.Decode(&r) != nil {
		return
	}
	n.store(r.Send)
	out.Encode(push{Send: n.log.Get(r.Want)})
}

// serveSync is the answering side.
func (n *Node) serveSync(stream network.Stream) {
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(syncTimeout))

	in := json.NewDecoder(stream)
	out := json.NewEncoder(stream)

	var h hello
	if in.Decode(&h) != nil || h.Room != n.room {
		return
	}
	if out.Encode(reply{Send: n.log.NotIn(h.Have), Want: n.log.Missing(h.Have)}) != nil {
		return
	}
	var p push
	if in.Decode(&p) != nil {
		return
	}
	n.store(p.Send)
}

// store saves messages learned from a peer.
func (n *Node) store(msgs []*Message) {
	for _, m := range msgs {
		if m == nil || m.Room != n.room {
			continue
		}
		isNew, err := n.log.Add(m)
		if err != nil || !isNew {
			continue
		}
		n.remember(m)
		n.deliver(m)
	}
}

// syncLoop re-syncs with every connected peer.
func (n *Node) syncLoop() {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-tick.C:
			for _, id := range n.host.Network().Peers() {
				n.syncWith(id)
			}
		}
	}
}
