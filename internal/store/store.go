// Package store holds every topic seen on the broker in memory.
//
// The ingest path does one map lookup and a few field writes per message.
// Readers never receive events; they poll with a cursor and get whatever
// changed since their last poll, so a slow reader costs nothing and loses
// nothing.
package store

import (
	"sync"
	"time"
)

const (
	// HistorySize is the number of messages kept for a selected topic.
	HistorySize = 500
	// DefaultMaxTopics bounds memory if a broker produces unbounded topic names.
	DefaultMaxTopics = 2_000_000
	// maxNamesPerPoll keeps a single frame of new topic names reasonably small.
	maxNamesPerPoll = 20_000
)

// Message is one received publish.
type Message struct {
	N           uint32 // running number of this message within its topic
	Time        time.Time
	Payload     []byte
	QoS         byte
	Retain      bool
	ContentType string
	UserProps   map[string]string
}

type topic struct {
	name     string
	count    uint32
	seq      uint64
	last     Message
	hist     *ring
	selected int // number of readers that have this topic selected
}

// Stats are connection-wide counters.
type Stats struct {
	Topics   int    `json:"topics"`
	Messages uint64 `json:"messages"`
	Bytes    uint64 `json:"bytes"`
	Dropped  uint64 `json:"dropped"` // messages for new topics rejected because MaxTopics was reached
}

// Store is safe for concurrent use.
type Store struct {
	mu        sync.RWMutex
	maxTopics int
	epoch     uint64
	seq       uint64
	byName    map[string]uint32
	topics    []*topic
	stats     Stats
}

func New(maxTopics int) *Store {
	if maxTopics <= 0 {
		maxTopics = DefaultMaxTopics
	}
	return &Store{maxTopics: maxTopics, epoch: 1, byName: make(map[string]uint32)}
}

// Ingest records a message. It is called from the MQTT receive loop and must stay cheap.
func (s *Store) Ingest(name string, m Message) {
	s.mu.Lock()
	id, ok := s.byName[name]
	if !ok {
		if len(s.topics) >= s.maxTopics {
			s.stats.Dropped++
			s.mu.Unlock()
			return
		}
		id = uint32(len(s.topics))
		s.topics = append(s.topics, &topic{name: name})
		s.byName[name] = id
	}
	t := s.topics[id]
	t.count++
	s.seq++
	t.seq = s.seq
	m.N = t.count
	t.last = m
	if t.hist != nil {
		t.hist.push(m)
	}
	s.stats.Messages++
	s.stats.Bytes += uint64(len(m.Payload))
	s.mu.Unlock()
}

// Reset drops all topics. Readers notice through the epoch change.
func (s *Store) Reset() {
	s.mu.Lock()
	s.epoch++
	s.seq = 0
	s.byName = make(map[string]uint32)
	s.topics = nil
	s.stats = Stats{}
	s.mu.Unlock()
}

// Select starts recording history for a topic, seeded with its latest message.
// It returns false if the id is unknown in the given epoch.
func (s *Store) Select(epoch uint64, id uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch || int(id) >= len(s.topics) {
		return false
	}
	t := s.topics[id]
	if t.selected == 0 {
		t.hist = newRing(HistorySize)
		t.hist.push(t.last)
	}
	t.selected++
	return true
}

// Release undoes one Select. History is dropped when nobody has the topic selected.
func (s *Store) Release(epoch uint64, id uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch || int(id) >= len(s.topics) {
		return
	}
	t := s.topics[id]
	if t.selected > 0 {
		t.selected--
		if t.selected == 0 {
			t.hist = nil
		}
	}
}

// Stats returns the connection-wide counters.
func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.stats
	st.Topics = len(s.topics)
	return st
}

// Name returns the topic name for an id in the current epoch.
func (s *Store) Name(id uint32) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if int(id) >= len(s.topics) {
		return "", false
	}
	return s.topics[id].name, true
}

// Cursor is a reader's position. The zero value means "send me everything".
type Cursor struct {
	Epoch  uint64
	Topics int    // number of topic names already delivered
	Seq    uint64 // store sequence already delivered
	HistN  uint32 // last history message delivered for the selected topic
}

// Preview is the latest message of a watched topic.
type Preview struct {
	ID  uint32
	Msg Message
}

// Update is everything that changed since the cursor.
type Update struct {
	Reset    bool // the store was reset; the reader must drop its state
	Epoch    uint64
	FirstID  uint32 // id of Names[0]; ids are dense, so Names[i] has id FirstID+i
	Names    []string
	More     bool     // more names are pending, poll again soon
	Counts   []uint32 // flat (id, count) pairs of topics that received messages
	Previews []Preview
	History  []Message
	Stats    Stats
}

// Poll advances the cursor and returns what changed.
//
// watch lists the topics whose latest message the reader wants (the rows it
// currently shows). With watchAll the previews are returned even if unchanged,
// which a reader asks for right after its watch list changed. selected is the
// topic id whose history is wanted, or -1.
func (s *Store) Poll(cur *Cursor, watch []uint32, watchAll bool, selected int64) Update {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u := Update{Epoch: s.epoch, Stats: s.stats}
	u.Stats.Topics = len(s.topics)
	if cur.Epoch != s.epoch {
		u.Reset = cur.Epoch != 0
		*cur = Cursor{Epoch: s.epoch}
		watchAll = true
	}

	known := cur.Topics
	if n := len(s.topics); known < n {
		end := n
		if end-known > maxNamesPerPoll {
			end = known + maxNamesPerPoll
			u.More = true
		}
		u.FirstID = uint32(known)
		u.Names = make([]string, 0, end-known)
		for _, t := range s.topics[known:end] {
			u.Names = append(u.Names, t.name)
		}
		cur.Topics = end
	}

	// Counts go out for topics the reader already knew that changed, and for
	// every topic whose name is delivered in this update.
	if s.seq > cur.Seq {
		for id, t := range s.topics[:known] {
			if t.seq > cur.Seq {
				u.Counts = append(u.Counts, uint32(id), t.count)
			}
		}
	}
	for id := known; id < cur.Topics; id++ {
		u.Counts = append(u.Counts, uint32(id), s.topics[id].count)
	}

	for _, id := range watch {
		if int(id) >= cur.Topics {
			continue
		}
		if t := s.topics[id]; watchAll || t.seq > cur.Seq {
			u.Previews = append(u.Previews, Preview{ID: id, Msg: t.last})
		}
	}

	if selected >= 0 && int(selected) < len(s.topics) {
		if t := s.topics[selected]; t.hist != nil {
			u.History = t.hist.after(cur.HistN)
			if len(u.History) > 0 {
				cur.HistN = u.History[len(u.History)-1].N
			}
		}
	}

	cur.Seq = s.seq
	return u
}

// ring is a fixed-size buffer of the most recent messages.
type ring struct {
	buf  []Message
	next int
	full bool
}

func newRing(size int) *ring { return &ring{buf: make([]Message, size)} }

func (r *ring) push(m Message) {
	r.buf[r.next] = m
	r.next++
	if r.next == len(r.buf) {
		r.next = 0
		r.full = true
	}
}

// after returns the messages with N greater than n, oldest first.
func (r *ring) after(n uint32) []Message {
	var out []Message
	appendFrom := func(ms []Message) {
		for _, m := range ms {
			if m.N > n {
				out = append(out, m)
			}
		}
	}
	if r.full {
		appendFrom(r.buf[r.next:])
	}
	appendFrom(r.buf[:r.next])
	return out
}
