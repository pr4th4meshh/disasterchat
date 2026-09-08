// Command disasterchat is a serverless, internet-free local network chat.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pr4th4meshh/disasterchat/internal/chat"
	"github.com/pr4th4meshh/disasterchat/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
	golog "github.com/ipfs/go-log/v2"
)

// addrList is a flag that may be repeated.
type addrList []string

func (a *addrList) String() string     { return strings.Join(*a, ",") }
func (a *addrList) Set(s string) error { *a = append(*a, s); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "disasterchat:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()

	var dial addrList
	port := flag.String("port", "0", "port to listen on (0 picks a free one)")
	room := flag.String("room", "chat-room", "room to join; must match on every device")
	nick := flag.String("nick", "", "your display name (defaults to this computer's name)")
	network := flag.String("network", "disasterchat", "discovery tag; must match on every device")
	dataDir := flag.String("data", filepath.Join(home, ".disasterchat"), "where to keep your key and messages")
	flag.Var(&dial, "peer", "address of a peer to connect to; may be repeated")
	flag.Parse()

	if *nick == "" {
		*nick, _ = os.Hostname()
		if *nick == "" {
			*nick = "anon"
		}
	}

	// libp2p and log would write over the interface; send it all to a file.
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(*dataDir, "disasterchat.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	golog.SetAllLoggers(golog.LevelFatal)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	node, err := chat.Start(ctx, chat.Options{
		Port:    *port,
		Room:    *room,
		Nick:    *nick,
		Network: *network,
		DataDir: *dataDir,
		Dial:    dial,
	})
	if err != nil {
		return err
	}
	defer node.Close()

	prog := tea.NewProgram(ui.New(node), tea.WithAltScreen())

	go func() {
		for {
			select {
			case <-ctx.Done():
				prog.Quit()
				return
			case m := <-node.Incoming:
				prog.Send(ui.Incoming{M: m})
			case text := <-node.Notices:
				prog.Send(ui.Notice(text))
			}
		}
	}()

	_, err = prog.Run()
	return err
}
