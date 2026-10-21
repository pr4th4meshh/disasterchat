package web

import (
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pr4th4meshh/disasterchat/internal/chat"
)

// start brings up a lone node and a web page in front of it.
func start(t *testing.T) (*chat.Node, *Server, *httptest.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n, err := chat.Start(ctx, chat.Options{
		Port: "0", Nick: "web",
		Room:    fmt.Sprintf("room-%d", rand.Int63()),
		Network: fmt.Sprintf("tag-%d", rand.Int63()),
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("starting node: %v", err)
	}
	t.Cleanup(func() { n.Close() })

	s := New(n)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return n, s, ts
}

func post(t *testing.T, ts *httptest.Server, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/send", strings.NewReader(body))
	req.Header.Set("X-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// TestNoTokenNoAccess: the page is public, nothing else is.
func TestNoTokenNoAccess(t *testing.T) {
	n, _, ts := start(t)

	if res := post(t, ts, "wrong", `{"text":"hello"}`); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("send with a bad token: got %d", res.StatusCode)
	}
	res, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events without a token: got %d", res.StatusCode)
	}
	if n.History().Len() != 0 {
		t.Fatal("a request without the token sent a message")
	}

	res, err = http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("page: got %d", res.StatusCode)
	}
}

// TestSendFromThePage: a line typed in the browser goes through the same
// commands as the terminal, and lands in history.
func TestSendFromThePage(t *testing.T) {
	n, s, ts := start(t)

	if res := post(t, ts, s.token, `{"text":"/sos trapped on the roof"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("send: got %d", res.StatusCode)
	}
	all := n.History().All()
	if len(all) != 1 || all[0].Kind != chat.KindSOS || all[0].Body != "trapped on the roof" {
		t.Fatalf("history after sending: %+v", all)
	}
}

// TestEventsReplayThenLive: a new page gets the history first, then
// whatever arrives after it connected.
func TestEventsReplayThenLive(t *testing.T) {
	n, s, ts := start(t)
	if _, err := n.Send(chat.KindChat, "said before the page opened"); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get(ts.URL + "/events?t=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	expect := func(want string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if strings.Contains(l, want) {
					return
				}
			case <-timeout:
				t.Fatalf("never saw %q", want)
			}
		}
	}

	expect("said before the page opened")
	expect("event: ready")

	m, err := n.Send(chat.KindStatus, "said after")
	if err != nil {
		t.Fatal(err)
	}
	s.Message(m)
	expect("said after")
}
