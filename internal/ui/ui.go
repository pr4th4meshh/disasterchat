// Package ui is the terminal interface: a transcript above a text box.
package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pr4th4meshh/disasterchat/internal/chat"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Incoming and Notice are what the node feeds in while it runs.
type Incoming struct{ M *chat.Message }
type Notice string

// tick keeps the header's peer count fresh.
type tick time.Time

var (
	header  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("88")).Padding(0, 1)
	dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	nameSty = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	mineSty = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	sosSty  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	resSty  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	staSty  = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
	bodySty = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	noteSty = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
)

// line is one row of the transcript: a message, or a note from us.
type line struct {
	at   int64
	msg  *chat.Message // nil for a note
	note string
}

type Model struct {
	node  *chat.Node
	view  viewport.Model
	input textinput.Model
	lines []line

	width, height int
	ready         bool
	done          bool
}

func New(n *chat.Node) Model {
	in := textinput.New()
	in.Placeholder = "type a message, or /help"
	in.Prompt = ""
	in.CharLimit = 1000
	in.Focus()

	u := Model{node: n, input: in}
	for _, m := range n.History().All() {
		u.lines = append(u.lines, line{at: m.At, msg: m})
	}
	u.say(fmt.Sprintf("you are %s in room %q, with %d saved messages",
		n.Nick(), n.Room(), n.History().Len()))
	u.say("looking for others on this network...")
	return u
}

// say adds a note from the app itself.
func (u *Model) say(text string) {
	u.lines = append(u.lines, line{at: time.Now().UnixMilli(), note: text})
}

// add puts a message in the transcript, in time order.
func (u *Model) add(m *chat.Message) {
	u.lines = append(u.lines, line{at: m.At, msg: m})
	sort.SliceStable(u.lines, func(i, j int) bool { return u.lines[i].at < u.lines[j].at })
}

func (u Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, everySecond())
}

func everySecond() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tick(t) })
}

func (u Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
		body := max(msg.Height-4, 3) // header, prompt, help
		if u.ready {
			u.view.Width, u.view.Height = msg.Width, body
		} else {
			u.view = viewport.New(msg.Width, body)
			u.ready = true
		}
		u.input.Width = msg.Width - 4
		u.redraw()

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return u, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(u.input.Value())
			u.input.SetValue("")
			if text != "" {
				u.handle(text)
				u.redraw()
			}
			if u.done {
				return u, tea.Quit
			}
			return u, nil
		}

	case Incoming:
		u.add(msg.M)
		u.redraw()

	case Notice:
		u.say(string(msg))
		u.redraw()

	case tick:
		u.redraw()
		cmds = append(cmds, everySecond())
	}

	var cmd tea.Cmd
	u.input, cmd = u.input.Update(msg)
	cmds = append(cmds, cmd)
	u.view, cmd = u.view.Update(msg)
	cmds = append(cmds, cmd)
	return u, tea.Batch(cmds...)
}

func (u *Model) redraw() {
	if !u.ready {
		return
	}
	bottom := u.view.AtBottom()

	var b strings.Builder
	for _, l := range u.lines {
		b.WriteString(u.render(l))
		b.WriteByte('\n')
	}
	u.view.SetContent(strings.TrimRight(b.String(), "\n"))

	if bottom {
		u.view.GotoBottom()
	}
}

// render turns one line into styled, wrapped text.
func (u Model) render(l line) string {
	wrap := lipgloss.NewStyle().Width(max(u.width-1, 20))
	when := dim.Render(time.UnixMilli(l.at).Format("15:04:05"))

	if l.msg == nil {
		return wrap.Render(when + " " + noteSty.Render("* "+l.note))
	}

	m := l.msg
	tag, body := "", bodySty.Render(m.Body)
	switch m.Kind {
	case chat.KindSOS:
		tag, body = sosSty.Render(" SOS "), sosSty.Render(m.Body)
	case chat.KindResource:
		tag, body = resSty.Render(" RES "), resSty.Render(m.Body)
	case chat.KindStatus:
		tag, body = staSty.Render(" STA "), staSty.Render(m.Body)
	}

	who := nameSty
	if m.Author == u.node.Self().String() {
		who = mineSty
	}
	where := ""
	if m.Lat != nil && m.Lon != nil {
		where = dim.Render(fmt.Sprintf(" @%.5f,%.5f", *m.Lat, *m.Lon))
	}
	return wrap.Render(when + tag + " " + who.Render(m.Nick) + dim.Render(":") + " " + body + where)
}

func (u Model) View() string {
	if !u.ready {
		return "starting..."
	}

	left := fmt.Sprintf("DisasterChat  room:%s  you:%s", u.node.Room(), u.node.Nick())
	right := fmt.Sprintf("peers:%d  messages:%d", u.node.PeerCount(), u.node.History().Len())
	if lat, lon := u.node.Location(); lat != nil && lon != nil {
		right += fmt.Sprintf("  @%.4f,%.4f", *lat, *lon)
	}
	gap := max(u.width-lipgloss.Width(left)-lipgloss.Width(right)-2, 1)

	return strings.Join([]string{
		header.Width(u.width).Render(left + strings.Repeat(" ", gap) + right),
		u.view.View(),
		mineSty.Render("> ") + u.input.View(),
		dim.Render("enter to send   /help for commands   ctrl+c to quit"),
	}, "\n")
}

// handle deals with one submitted line: a command, or a message.
func (u *Model) handle(text string) {
	if !strings.HasPrefix(text, "/") {
		u.send(chat.KindChat, text)
		return
	}
	cmd, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "/sos":
		u.needsText(rest, "/sos <what you need and where you are>", chat.KindSOS)
	case "/res":
		u.needsText(rest, "/res <a resource you have or need>", chat.KindResource)
	case "/status":
		u.needsText(rest, "/status <situation update>", chat.KindStatus)

	case "/nick":
		if rest == "" {
			u.say("usage: /nick <name>")
			return
		}
		u.node.SetNick(rest)
		u.say("you are now " + rest)

	case "/loc":
		u.setLocation(rest)

	case "/peers":
		names := u.node.Peers()
		if len(names) == 0 {
			u.say("not connected to anyone yet")
		}
		for _, name := range names {
			u.say("connected to " + name)
		}

	case "/me":
		u.say("your id: " + u.node.Self().String())
		for _, a := range u.node.Addrs() {
			u.say("your address: " + a)
		}

	case "/dial":
		if rest == "" {
			u.say("usage: /dial <an address from someone else's /me>")
			return
		}
		if err := u.node.Dial(rest); err != nil {
			u.say(err.Error())
		}

	case "/help":
		for _, l := range []string{
			"/sos <text>       an emergency request",
			"/res <text>       offer or ask for a resource (water, fuel, medicine)",
			"/status <text>    a situation update",
			"/nick <name>      change your display name",
			"/loc <lat> <lon>  tag your messages with coordinates ( /loc clear to stop )",
			"/peers            who you are connected to",
			"/me               your id and address, to give to someone else",
			"/dial <address>   connect by hand, if this network blocks discovery",
			"/quit             exit",
		} {
			u.say(l)
		}

	case "/quit":
		u.done = true

	default:
		u.say("no such command " + cmd + " -- try /help")
	}
}

func (u *Model) needsText(rest, usage string, kind chat.Kind) {
	if rest == "" {
		u.say("usage: " + usage)
		return
	}
	u.send(kind, rest)
}

func (u *Model) send(kind chat.Kind, body string) {
	m, err := u.node.Send(kind, body)
	if err != nil {
		u.say(err.Error())
	}
	if m != nil {
		u.add(m)
	}
}

func (u *Model) setLocation(rest string) {
	if rest == "" || rest == "clear" {
		u.node.SetLocation(nil, nil)
		u.say("location cleared")
		return
	}
	parts := strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == ',' })
	if len(parts) != 2 {
		u.say("usage: /loc <lat> <lon>   or   /loc clear")
		return
	}
	lat, err1 := strconv.ParseFloat(parts[0], 64)
	lon, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		u.say("those do not look like valid coordinates")
		return
	}
	u.node.SetLocation(&lat, &lon)
	u.say(fmt.Sprintf("location set to %.5f, %.5f", lat, lon))
}
