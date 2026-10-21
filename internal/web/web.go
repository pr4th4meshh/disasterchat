// Package web is a browser interface to the same node the terminal shows:
// one page, live updates over server-sent events, and a POST to send.
// Everything it serves is inside the binary, so it works with no internet.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pr4th4meshh/disasterchat/internal/chat"
	"github.com/pr4th4meshh/disasterchat/internal/command"
)

//go:embed index.html
var page []byte

// event is one server-sent event, already encoded.
type event struct {
	name string
	data []byte
}

// message is what the page sees of a chat.Message: everything but the
// signature, which the node has already checked.
type message struct {
	ID     string    `json:"id"`
	Kind   chat.Kind `json:"kind"`
	Body   string    `json:"body"`
	Nick   string    `json:"nick"`
	Author string    `json:"author"`
	At     int64     `json:"at"`
	Lat    *float64  `json:"lat,omitempty"`
	Lon    *float64  `json:"lon,omitempty"`
}

type state struct {
	Room     string   `json:"room"`
	Nick     string   `json:"nick"`
	Self     string   `json:"self"`
	Peers    int      `json:"peers"`
	Messages int      `json:"messages"`
	Lat      *float64 `json:"lat,omitempty"`
	Lon      *float64 `json:"lon,omitempty"`
}

type Server struct {
	node  *chat.Node
	token string

	mu      sync.Mutex
	clients map[chan event]bool
}

func New(n *chat.Node) *Server {
	return &Server{node: n, token: newToken(), clients: map[chan event]bool{}}
}

// newToken is the secret in the link. Whoever has it can post as you.
func newToken() string {
	b := make([]byte, 9)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("POST /send", s.send)
	return mux
}

// Serve listens on addr until ctx ends, and returns the links to open.
func (s *Server) Serve(ctx context.Context, addr string) ([]string, error) {
	if _, port, err := net.SplitHostPort(addr); err != nil || !isPort(port) {
		return nil, fmt.Errorf("--web %q is not an address; try --web :8080 or --web 127.0.0.1:8080", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("starting web page: %w", err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	go func() {
		<-ctx.Done()
		s.closeAll()
		srv.Close()
	}()
	return s.links(ln.Addr().(*net.TCPAddr)), nil
}

func isPort(s string) bool {
	p, err := strconv.Atoi(s)
	return err == nil && p >= 0 && p <= 65535
}

// links are the URLs that reach this listener, token included.
func (s *Server) links(a *net.TCPAddr) []string {
	var hosts []string
	if a.IP.IsUnspecified() {
		hosts = append(hosts, "localhost")
		addrs, _ := net.InterfaceAddrs()
		for _, ad := range addrs {
			if ip, ok := ad.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLoopback() {
				hosts = append(hosts, ip.IP.String())
			}
		}
	} else {
		hosts = append(hosts, a.IP.String())
	}

	var out []string
	for _, h := range hosts {
		out = append(out, fmt.Sprintf("http://%s/?t=%s", net.JoinHostPort(h, fmt.Sprint(a.Port)), s.token))
	}
	return out
}

func (s *Server) allowed(r *http.Request) bool {
	t := r.Header.Get("X-Token")
	if t == "" {
		t = r.URL.Query().Get("t")
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Write(page)
}

// events streams the room: who we are, the whole history, then live updates.
// A browser that reconnects gets the history again and drops duplicates.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(r) {
		http.Error(w, "this link is missing its token; open the one printed in the chat", http.StatusUnauthorized)
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	// subscribe before reading history, so nothing falls in the gap
	ch := s.subscribe()
	defer s.unsubscribe(ch)

	write := func(e event) error {
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
		return err
	}
	write(s.stateEvent())
	for _, m := range s.node.History().All() {
		write(messageEvent(m))
	}
	write(event{"ready", []byte("{}")})
	flush.Flush()

	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			err = write(e)
		case <-tick.C:
			err = write(s.stateEvent())
		}
		if err != nil {
			return
		}
		flush.Flush()
	}
}

// send runs one typed line, exactly as if it was typed in the terminal.
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(r) {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var notes []string
	text := strings.TrimSpace(in.Text)
	if text == "/quit" {
		notes = []string{"close this tab to leave; the node keeps running"}
	} else if text != "" {
		notes = command.Run(s.node, text).Notes
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"notes": notes})
}

// Message passes a new room message to every open page.
func (s *Server) Message(m *chat.Message) { s.broadcast(messageEvent(m)) }

// Notice passes a status line to every open page.
func (s *Server) Notice(text string) {
	b, _ := json.Marshal(text)
	s.broadcast(event{"notice", b})
}

// broadcast never blocks. A page that cannot keep up is dropped; its browser
// reconnects and catches up from history.
func (s *Server) broadcast(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- e:
		default:
			delete(s.clients, ch)
			close(ch)
		}
	}
}

func (s *Server) subscribe() chan event {
	ch := make(chan event, 256)
	s.mu.Lock()
	s.clients[ch] = true
	s.mu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[ch] {
		delete(s.clients, ch)
		close(ch)
	}
}

func (s *Server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		delete(s.clients, ch)
		close(ch)
	}
}

func (s *Server) stateEvent() event {
	lat, lon := s.node.Location()
	b, _ := json.Marshal(state{
		Room: s.node.Room(), Nick: s.node.Nick(), Self: s.node.Self().String(),
		Peers: s.node.PeerCount(), Messages: s.node.History().Len(),
		Lat: lat, Lon: lon,
	})
	return event{"state", b}
}

func messageEvent(m *chat.Message) event {
	b, _ := json.Marshal(message{
		ID: m.ID, Kind: m.Kind, Body: m.Body, Nick: m.Nick,
		Author: m.Author, At: m.At, Lat: m.Lat, Lon: m.Lon,
	})
	return event{"msg", b}
}
