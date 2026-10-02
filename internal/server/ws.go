package server

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/silasmartin/mqttex/internal/broker"
	"github.com/silasmartin/mqttex/internal/store"
)

const (
	tickInterval   = 200 * time.Millisecond
	writeTimeout   = 10 * time.Second
	previewBytes   = 160
	historyBytes   = 256 << 10
	maxWatchedRows = 2000
	maxCompared    = 32       // topics in a comparison; the table is meant for a handful of devices
	compareBytes   = 64 << 10 // far above previewBytes: the table needs whole JSON documents

	frameCounts = 1 // binary frame: uint32 LE [frameCounts, n, id, count, id, count, ...]
)

// wirePayload is a payload as sent to the browser: text when it is valid
// UTF-8, base64 otherwise, cut to a maximum length.
type wirePayload struct {
	Text   *string `json:"s,omitempty"`
	Base64 string  `json:"b,omitempty"`
	Size   int     `json:"size"`
	Trunc  bool    `json:"trunc,omitempty"`
}

func encodePayload(p []byte, max int) wirePayload {
	w := wirePayload{Size: len(p)}
	if len(p) > max {
		w.Trunc = true
		cut := max
		for cut > 0 && !utf8.RuneStart(p[cut]) {
			cut--
		}
		p = p[:cut]
	}
	if utf8.Valid(p) {
		s := string(p)
		w.Text = &s
	} else {
		w.Base64 = base64.StdEncoding.EncodeToString(p)
	}
	return w
}

type wirePreview struct {
	ID uint32 `json:"id"`
	wirePayload
	Time   int64 `json:"ts"` // unix milliseconds
	Retain bool  `json:"retain,omitempty"`
}

func wirePreviews(ps []store.Preview, max int) []wirePreview {
	var out []wirePreview
	for _, p := range ps {
		out = append(out, wirePreview{ID: p.ID, wirePayload: encodePayload(p.Msg.Payload, max),
			Time: p.Msg.Time.UnixMilli(), Retain: p.Msg.Retain})
	}
	return out
}

type wireMessage struct {
	N uint32 `json:"n"`
	wirePayload
	Time        int64             `json:"ts"`
	QoS         byte              `json:"qos"`
	Retain      bool              `json:"retain,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	UserProps   map[string]string `json:"userProps,omitempty"`
}

type wireTick struct {
	Type     string        `json:"t"`
	Reset    bool          `json:"reset,omitempty"`
	Epoch    uint64        `json:"epoch"`
	First    uint32        `json:"first"`
	Names    []string      `json:"names,omitempty"`
	Previews []wirePreview `json:"previews,omitempty"`
	Compare  []wirePreview `json:"compare,omitempty"`
	Selected int64         `json:"selected"`
	History  []wireMessage `json:"history,omitempty"`
	Stats    store.Stats   `json:"stats"`
	Status   broker.Status `json:"status"`
}

// clientMsg is what the browser sends: the rows it shows and the topic it has open.
type clientMsg struct {
	Type  string   `json:"t"` // "watch", "select" or "compare"
	Epoch uint64   `json:"epoch"`
	IDs   []uint32 `json:"ids"`
	ID    int64    `json:"id"`
}

type wsClient struct {
	mu       sync.Mutex
	cur      store.Cursor
	want     store.Want // want.Selected is set while the store holds the selection
	selEpoch uint64     // epoch in which want.Selected was selected
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	// Accept verifies that the Origin header matches the Host header.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := &wsClient{}
	defer func() {
		c.mu.Lock()
		if c.want.Selected != nil {
			s.store.Release(c.selEpoch, *c.want.Selected)
		}
		c.mu.Unlock()
	}()

	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m clientMsg
			if json.Unmarshal(data, &m) == nil {
				s.handleClientMsg(c, m)
			}
		}
	}()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		for more := true; more; {
			if more, err = s.sendUpdate(ctx, conn, c); err != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) handleClientMsg(c *wsClient, m clientMsg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m.Type {
	case "watch":
		if m.Epoch != c.cur.Epoch {
			return
		}
		if len(m.IDs) > maxWatchedRows {
			m.IDs = m.IDs[:maxWatchedRows]
		}
		c.want.Watch, c.want.WatchAll = m.IDs, true
	case "compare":
		if m.Epoch != c.cur.Epoch {
			return
		}
		if len(m.IDs) > maxCompared {
			m.IDs = m.IDs[:maxCompared]
		}
		c.want.Compare, c.want.CompareAll = m.IDs, true
	case "select":
		if c.want.Selected != nil {
			s.store.Release(c.selEpoch, *c.want.Selected)
			c.want.Selected = nil
		}
		if id := uint32(m.ID); m.ID >= 0 && s.store.Select(m.Epoch, id) {
			c.want.Selected, c.selEpoch = &id, m.Epoch
		}
		c.cur.HistN = 0
	}
}

// sendUpdate polls the store and writes what changed. Names go out before the
// counts frame so the browser always knows an id before it sees its count.
func (s *Server) sendUpdate(ctx context.Context, conn *websocket.Conn, c *wsClient) (more bool, err error) {
	c.mu.Lock()
	u := s.store.Poll(&c.cur, c.want)
	c.want.WatchAll, c.want.CompareAll = false, false
	if u.Reset {
		// Ids from before the reset mean nothing now.
		c.want = store.Want{}
	}
	selected := int64(-1)
	if c.want.Selected != nil {
		selected = int64(*c.want.Selected)
	}
	c.mu.Unlock()

	tick := wireTick{Type: "tick", Reset: u.Reset, Epoch: u.Epoch, First: u.FirstID, Names: u.Names,
		Previews: wirePreviews(u.Previews, previewBytes), Compare: wirePreviews(u.Compared, compareBytes),
		Selected: selected, Stats: u.Stats, Status: s.broker.Status()}
	for _, m := range u.History {
		tick.History = append(tick.History, wireMessage{N: m.N, wirePayload: encodePayload(m.Payload, historyBytes),
			Time: m.Time.UnixMilli(), QoS: m.QoS, Retain: m.Retain, ContentType: m.ContentType, UserProps: m.UserProps})
	}
	data, err := json.Marshal(tick)
	if err != nil {
		return false, err
	}
	if err = write(ctx, conn, websocket.MessageText, data); err != nil {
		return false, err
	}
	if len(u.Counts) > 0 {
		frame := make([]byte, 8+4*len(u.Counts))
		binary.LittleEndian.PutUint32(frame, frameCounts)
		binary.LittleEndian.PutUint32(frame[4:], uint32(len(u.Counts)/2))
		for i, v := range u.Counts {
			binary.LittleEndian.PutUint32(frame[8+4*i:], v)
		}
		if err = write(ctx, conn, websocket.MessageBinary, frame); err != nil {
			return false, err
		}
	}
	return u.More, nil
}

func write(ctx context.Context, conn *websocket.Conn, typ websocket.MessageType, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, typ, data)
}
