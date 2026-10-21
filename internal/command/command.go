// Package command turns one typed line into an action on the node, so the
// terminal and the web page understand exactly the same commands.
package command

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pr4th4meshh/disasterchat/internal/chat"
)

// Result is what one line did.
type Result struct {
	Sent  *chat.Message // the message sent, if any
	Notes []string      // replies for whoever typed it
	Quit  bool
}

// Help is the /help text, one command per line.
var Help = []string{
	"/sos <text>       an emergency request",
	"/res <text>       offer or ask for a resource (water, fuel, medicine)",
	"/status <text>    a situation update",
	"/nick <name>      change your display name",
	"/loc <lat> <lon>  tag your messages with coordinates ( /loc clear to stop )",
	"/peers            who you are connected to",
	"/me               your id and address, to give to someone else",
	"/dial <address>   connect by hand, if this network blocks discovery",
	"/quit             exit",
}

// Run deals with one submitted line: a command, or a message.
func Run(n *chat.Node, text string) Result {
	var r Result
	if !strings.HasPrefix(text, "/") {
		r.send(n, chat.KindChat, text)
		return r
	}
	cmd, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "/sos":
		r.needsText(n, rest, "/sos <what you need and where you are>", chat.KindSOS)
	case "/res":
		r.needsText(n, rest, "/res <a resource you have or need>", chat.KindResource)
	case "/status":
		r.needsText(n, rest, "/status <situation update>", chat.KindStatus)

	case "/nick":
		if rest == "" {
			r.say("usage: /nick <name>")
			break
		}
		n.SetNick(rest)
		r.say("you are now " + rest)

	case "/loc":
		r.setLocation(n, rest)

	case "/peers":
		names := n.Peers()
		if len(names) == 0 {
			r.say("not connected to anyone yet")
		}
		for _, name := range names {
			r.say("connected to " + name)
		}

	case "/me":
		r.say("your id: " + n.Self().String())
		for _, a := range n.Addrs() {
			r.say("your address: " + a)
		}

	case "/dial":
		if rest == "" {
			r.say("usage: /dial <an address from someone else's /me>")
			break
		}
		if err := n.Dial(rest); err != nil {
			r.say(err.Error())
		}

	case "/help":
		r.Notes = append(r.Notes, Help...)

	case "/quit":
		r.Quit = true

	default:
		r.say("no such command " + cmd + " -- try /help")
	}
	return r
}

func (r *Result) say(text string) { r.Notes = append(r.Notes, text) }

func (r *Result) needsText(n *chat.Node, rest, usage string, kind chat.Kind) {
	if rest == "" {
		r.say("usage: " + usage)
		return
	}
	r.send(n, kind, rest)
}

func (r *Result) send(n *chat.Node, kind chat.Kind, body string) {
	m, err := n.Send(kind, body)
	if err != nil {
		r.say(err.Error())
	}
	r.Sent = m
}

func (r *Result) setLocation(n *chat.Node, rest string) {
	if rest == "" || rest == "clear" {
		n.SetLocation(nil, nil)
		r.say("location cleared")
		return
	}
	parts := strings.FieldsFunc(rest, func(c rune) bool { return c == ' ' || c == ',' })
	if len(parts) != 2 {
		r.say("usage: /loc <lat> <lon>   or   /loc clear")
		return
	}
	lat, err1 := strconv.ParseFloat(parts[0], 64)
	lon, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		r.say("those do not look like valid coordinates")
		return
	}
	n.SetLocation(&lat, &lon)
	r.say(fmt.Sprintf("location set to %.5f, %.5f", lat, lon))
}
