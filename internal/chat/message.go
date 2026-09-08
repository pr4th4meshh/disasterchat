package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

type Kind string

const (
	KindChat     Kind = "chat"
	KindSOS      Kind = "sos"
	KindResource Kind = "resource"
	KindStatus   Kind = "status"
)

// Message is one broadcast in a room.
type Message struct {
	ID     string   `json:"id"`
	Room   string   `json:"room"`
	Kind   Kind     `json:"kind"`
	Body   string   `json:"body"`
	Nick   string   `json:"nick"`
	Author string   `json:"author"` // the sender's peer ID
	At     int64    `json:"at"`     // unix milliseconds
	Lat    *float64 `json:"lat,omitempty"`
	Lon    *float64 `json:"lon,omitempty"`
	Sig    []byte   `json:"sig"`
}

// signedBytes is what we hash and sign: the message without ID and Sig.
func (m *Message) signedBytes() ([]byte, error) {
	copy := *m
	copy.ID, copy.Sig = "", nil
	return json.Marshal(copy)
}

// NewMessage builds a message and signs it with your key.
func NewMessage(key crypto.PrivKey, self peer.ID, room, nick string, kind Kind, body string, lat, lon *float64) (*Message, error) {
	m := &Message{
		Room:   room,
		Kind:   kind,
		Body:   body,
		Nick:   nick,
		Author: self.String(),
		At:     time.Now().UnixMilli(),
		Lat:    lat,
		Lon:    lon,
	}

	b, err := m.signedBytes()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	m.ID = hex.EncodeToString(sum[:])

	m.Sig, err = key.Sign(b)
	if err != nil {
		return nil, fmt.Errorf("signing message: %w", err)
	}
	return m, nil
}

// Verify checks the message came from Author and was not edited.
func (m *Message) Verify() error {
	if m.Body == "" {
		return errors.New("message has no text")
	}
	id, err := peer.Decode(m.Author)
	if err != nil {
		return fmt.Errorf("bad author id: %w", err)
	}
	pub, err := id.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("cannot get author's public key: %w", err)
	}

	b, err := m.signedBytes()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != m.ID {
		return errors.New("message id does not match its contents")
	}

	ok, err := pub.Verify(b, m.Sig)
	if err != nil {
		return fmt.Errorf("checking signature: %w", err)
	}
	if !ok {
		return errors.New("bad signature")
	}
	return nil
}

func (m *Message) Time() time.Time { return time.UnixMilli(m.At) }
