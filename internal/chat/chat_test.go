package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startTestNode brings up a node in a throwaway directory.
func startTestNode(t *testing.T, ctx context.Context, room, tag, nick string) *Node {
	t.Helper()
	n, err := Start(ctx, Options{
		Port: "0", Room: room, Network: tag, Nick: nick, DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("starting %s: %v", nick, err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

// waitFor polls until cond is true, or gives up.
func waitFor(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

func has(n *Node, body string) bool {
	for _, m := range n.History().All() {
		if m.Body == body {
			return true
		}
	}
	return false
}

func localAddr(t *testing.T, n *Node) string {
	t.Helper()
	for _, a := range n.Addrs() {
		if strings.HasPrefix(a, "/ip4/127.0.0.1/tcp/") {
			return a
		}
	}
	t.Fatalf("no loopback address in %v", n.Addrs())
	return ""
}

// names gives each test its own room and discovery tag, so tests cannot see
// each other's traffic -- or a real DisasterChat running on the same network.
func names() (room, tag string) {
	return fmt.Sprintf("room-%d", rand.Int63()), fmt.Sprintf("tag-%d", rand.Int63())
}

// TestLateJoinerGetsHistory is the important one. Pubsub cannot deliver a
// message to someone who was not there when it was sent, so if the third node
// ends up with the message, only the sync in sync.go can have put it there.
func TestLateJoinerGetsHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	room, tag := names()

	alpha := startTestNode(t, ctx, room, tag, "alpha")
	bravo := startTestNode(t, ctx, room, tag, "bravo")

	// Connect by hand: mDNS needs permissions the test machine may not have.
	if err := bravo.Dial(localAddr(t, alpha)); err != nil {
		t.Fatalf("bravo could not reach alpha: %v", err)
	}
	waitFor(t, "alpha and bravo to connect", 10*time.Second, func() bool {
		return alpha.PeerCount() > 0 && bravo.PeerCount() > 0
	})

	// Keep sending until it lands: gossip takes a moment to settle.
	const body = "bridge on the main road is out"
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := 0; i < 20 && !has(bravo, body); i++ {
			alpha.Send(KindSOS, body)
			time.Sleep(400 * time.Millisecond)
		}
	}()
	waitFor(t, "bravo to receive the message", 15*time.Second, func() bool { return has(bravo, body) })
	<-sent
	time.Sleep(time.Second) // let any last broadcast finish

	charlie := startTestNode(t, ctx, room, tag, "charlie")
	if charlie.History().Len() != 0 {
		t.Fatalf("a new node should start empty, has %d", charlie.History().Len())
	}
	if err := charlie.Dial(localAddr(t, bravo)); err != nil {
		t.Fatalf("charlie could not reach bravo: %v", err)
	}
	waitFor(t, "charlie to catch up on history", 20*time.Second, func() bool { return has(charlie, body) })

	// Bravo passed the message on, but alpha still signed it.
	for _, m := range charlie.History().All() {
		if m.Body != body {
			continue
		}
		if err := m.Verify(); err != nil {
			t.Fatalf("relayed message no longer verifies: %v", err)
		}
		if m.Author != alpha.Self().String() {
			t.Fatalf("author should still be alpha, got %s", m.Author)
		}
		if m.Kind != KindSOS {
			t.Fatalf("kind should survive the relay, got %s", m.Kind)
		}
	}
}

// TestEditedMessageIsRejected shows why messages are signed: a peer passing a
// message along cannot change what it says.
func TestEditedMessageIsRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	room, tag := names()

	n := startTestNode(t, ctx, room, tag, "alpha")
	m, err := n.Send(KindChat, "meet at the school")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := m.Verify(); err != nil {
		t.Fatalf("a freshly sent message should verify: %v", err)
	}

	edited := *m
	edited.Body = "meet at the river"
	if err := edited.Verify(); err == nil {
		t.Fatal("an edited message should not verify")
	}

	// Rehashing it does not help, because the signature still will not match.
	edited.ID = ""
	if err := edited.Verify(); err == nil {
		t.Fatal("a rehashed edited message should not verify")
	}
}

// TestDiscovery checks that two nodes find each other over the local network
// with no addresses given and no help.
//
// If this one fails, something is blocking mDNS. On macOS that is usually the
// Local Network permission: allow your terminal under System Settings >
// Privacy & Security > Local Network. Guest and corporate wifi often block
// peer-to-peer traffic outright.
func TestDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	room, tag := names()

	alpha := startTestNode(t, ctx, room, tag, "alpha")
	bravo := startTestNode(t, ctx, room, tag, "bravo")

	waitFor(t, "the two nodes to find each other", 30*time.Second, func() bool {
		return alpha.PeerCount() > 0 && bravo.PeerCount() > 0
	})
}

// TestEditingTheFileOnDiskDropsTheMessage is the signing property seen from
// the other side: the message log is a plain text file anyone can edit, and an
// edited message simply stops existing, because only the author's key can
// produce a signature that matches.
func TestEditingTheFileOnDiskDropsTheMessage(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room, tag := names()
	n, err := Start(ctx, Options{Port: "0", Room: room, Network: tag, Nick: "alpha", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	n.Send(KindChat, "meet at the school")
	n.Send(KindChat, "bring the radio")
	n.Close()

	path := filepath.Join(dir, "rooms", room+".jsonl")
	raw, _ := os.ReadFile(path)
	edited := strings.Replace(string(raw), "meet at the school", "meet at the river", 1)
	os.WriteFile(path, []byte(edited), 0o600)

	// Reopen, the way the program does at startup.
	h, err := OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	var bodies []string
	for _, m := range h.All() {
		bodies = append(bodies, m.Body)
	}
	t.Logf("after editing the file, history holds: %v", bodies)

	if len(bodies) != 1 || bodies[0] != "bring the radio" {
		t.Fatalf("expected only the untouched message to survive, got %v", bodies)
	}
	var check map[string]any
	json.Unmarshal([]byte(strings.Split(edited, "\n")[0]), &check)
	if check["body"] != "meet at the river" {
		t.Fatal("test setup wrong: the edit did not land in the file")
	}
}
