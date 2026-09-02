package chat

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// History is every message this node has seen, kept on disk as one JSON
// message per line.
type History struct {
	mu   sync.Mutex
	file *os.File
	byID map[string]*Message
	all  []*Message // oldest first
}

// OpenHistory loads the log at path, creating it if needed.
func OpenHistory(path string) (*History, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	h := &History{byID: map[string]*Message{}}

	if f, err := os.Open(path); err == nil {
		lines := bufio.NewScanner(f)
		lines.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for lines.Scan() {
			var m Message
			if json.Unmarshal(lines.Bytes(), &m) != nil {
				continue
			}
			if m.Verify() != nil {
				continue
			}
			h.keep(&m)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	h.file = f
	return h, nil
}

// keep puts a message in memory, in time order. Callers hold the lock.
func (h *History) keep(m *Message) {
	h.byID[m.ID] = m
	h.all = append(h.all, m)
	sort.SliceStable(h.all, func(i, j int) bool { return h.all[i].At < h.all[j].At })
}

// Add checks a message and stores it, reporting whether it was new.
func (h *History) Add(m *Message) (bool, error) {
	if err := m.Verify(); err != nil {
		return false, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, seen := h.byID[m.ID]; seen {
		return false, nil
	}

	line, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	if _, err := h.file.Write(append(line, '\n')); err != nil {
		return false, err
	}
	h.keep(m)
	return true, nil
}

// All returns every message, oldest first.
func (h *History) All() []*Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Message, len(h.all))
	copy(out, h.all)
	return out
}

// IDs lists what we hold.
func (h *History) IDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.all))
	for _, m := range h.all {
		ids = append(ids, m.ID)
	}
	return ids
}

// Missing returns the ids from theirs that we do not have.
func (h *History) Missing(theirs []string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, id := range theirs {
		if _, have := h.byID[id]; !have {
			out = append(out, id)
		}
	}
	return out
}

// NotIn returns our messages whose ids are not in theirs.
func (h *History) NotIn(theirs []string) []*Message {
	have := make(map[string]bool, len(theirs))
	for _, id := range theirs {
		have[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*Message
	for _, m := range h.all {
		if !have[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// Get returns the messages for these ids that we actually hold.
func (h *History) Get(ids []string) []*Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Message, 0, len(ids))
	for _, id := range ids {
		if m, ok := h.byID[id]; ok {
			out = append(out, m)
		}
	}
	return out
}

func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.all)
}

func (h *History) Close() error { return h.file.Close() }
