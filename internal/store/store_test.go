package store

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func msg(p string) Message { return Message{Payload: []byte(p)} }

func countsMap(pairs []uint32) map[uint32]uint32 {
	m := map[uint32]uint32{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

func TestPollDeliversNamesOnceAndOnlyChangedCounts(t *testing.T) {
	s := New(0)
	s.Ingest("/topic/a", msg("1"))
	s.Ingest("/topic/b", msg("1"))
	s.Ingest("/topic/a", msg("2"))

	var cur Cursor
	u := s.Poll(&cur, nil, false, -1)
	if u.Reset {
		t.Fatal("first poll must not report a reset")
	}
	if len(u.Names) != 2 || u.Names[0] != "/topic/a" || u.Names[1] != "/topic/b" || u.FirstID != 0 {
		t.Fatalf("names = %v first = %d", u.Names, u.FirstID)
	}
	if c := countsMap(u.Counts); c[0] != 2 || c[1] != 1 {
		t.Fatalf("counts = %v", c)
	}
	if u.Stats.Topics != 2 || u.Stats.Messages != 3 {
		t.Fatalf("stats = %+v", u.Stats)
	}

	if u = s.Poll(&cur, nil, false, -1); len(u.Names) != 0 || len(u.Counts) != 0 {
		t.Fatalf("idle poll returned names=%v counts=%v", u.Names, u.Counts)
	}

	s.Ingest("/topic/b", msg("2"))
	s.Ingest("/topic/c", msg("1"))
	u = s.Poll(&cur, nil, false, -1)
	if len(u.Names) != 1 || u.FirstID != 2 {
		t.Fatalf("names = %v first = %d", u.Names, u.FirstID)
	}
	if c := countsMap(u.Counts); len(c) != 2 || c[1] != 2 || c[2] != 1 {
		t.Fatalf("counts = %v", c)
	}
}

func TestPollChunksNamesWithoutLosingCounts(t *testing.T) {
	s := New(0)
	total := maxNamesPerPoll + 10
	for i := 0; i < total; i++ {
		s.Ingest(fmt.Sprintf("t/%d", i), msg("x"))
	}
	var cur Cursor
	u := s.Poll(&cur, nil, false, -1)
	if !u.More || len(u.Names) != maxNamesPerPoll {
		t.Fatalf("more=%v names=%d", u.More, len(u.Names))
	}
	seen := len(u.Counts) / 2
	u = s.Poll(&cur, nil, false, -1)
	if u.More || len(u.Names) != 10 || int(u.FirstID) != maxNamesPerPoll {
		t.Fatalf("more=%v names=%d first=%d", u.More, len(u.Names), u.FirstID)
	}
	if seen += len(u.Counts) / 2; seen != total {
		t.Fatalf("counts delivered for %d of %d topics", seen, total)
	}
}

func TestPreviewsOnlyForWatchedAndChanged(t *testing.T) {
	s := New(0)
	s.Ingest("a", msg("a1"))
	s.Ingest("b", msg("b1"))
	var cur Cursor
	s.Poll(&cur, nil, false, -1)

	u := s.Poll(&cur, []uint32{1}, true, -1)
	if len(u.Previews) != 1 || u.Previews[0].ID != 1 || string(u.Previews[0].Msg.Payload) != "b1" {
		t.Fatalf("previews = %+v", u.Previews)
	}
	if u = s.Poll(&cur, []uint32{1}, false, -1); len(u.Previews) != 0 {
		t.Fatalf("unchanged watched topic was resent: %+v", u.Previews)
	}
	s.Ingest("a", msg("a2"))
	if u = s.Poll(&cur, []uint32{1}, false, -1); len(u.Previews) != 0 {
		t.Fatalf("unwatched topic leaked: %+v", u.Previews)
	}
	s.Ingest("b", msg("b2"))
	if u = s.Poll(&cur, []uint32{1, 99}, false, -1); len(u.Previews) != 1 || string(u.Previews[0].Msg.Payload) != "b2" {
		t.Fatalf("previews = %+v", u.Previews)
	}
}

func TestHistoryOnlyWhileSelected(t *testing.T) {
	s := New(0)
	s.Ingest("a", msg("1"))
	s.Ingest("a", msg("2"))
	var cur Cursor
	s.Poll(&cur, nil, false, -1)

	if u := s.Poll(&cur, nil, false, 0); len(u.History) != 0 {
		t.Fatalf("history without select: %+v", u.History)
	}
	if !s.Select(cur.Epoch, 0) {
		t.Fatal("select failed")
	}
	u := s.Poll(&cur, nil, false, 0)
	if len(u.History) != 1 || string(u.History[0].Payload) != "2" || u.History[0].N != 2 {
		t.Fatalf("seed history = %+v", u.History)
	}
	s.Ingest("a", msg("3"))
	s.Ingest("a", msg("4"))
	u = s.Poll(&cur, nil, false, 0)
	if len(u.History) != 2 || u.History[0].N != 3 || u.History[1].N != 4 {
		t.Fatalf("history = %+v", u.History)
	}

	// A second reader shares the recording; it ends with the last release.
	s.Select(cur.Epoch, 0)
	s.Release(cur.Epoch, 0)
	s.Ingest("a", msg("5"))
	if u = s.Poll(&cur, nil, false, 0); len(u.History) != 1 {
		t.Fatalf("history after partial release = %+v", u.History)
	}
	s.Release(cur.Epoch, 0)
	s.Ingest("a", msg("6"))
	if u = s.Poll(&cur, nil, false, 0); len(u.History) != 0 {
		t.Fatalf("history after release = %+v", u.History)
	}
}

func TestRingKeepsNewest(t *testing.T) {
	r := newRing(3)
	for i := uint32(1); i <= 5; i++ {
		r.push(Message{N: i})
	}
	got := r.after(0)
	if len(got) != 3 || got[0].N != 3 || got[2].N != 5 {
		t.Fatalf("ring = %+v", got)
	}
	if got = r.after(4); len(got) != 1 || got[0].N != 5 {
		t.Fatalf("ring after 4 = %+v", got)
	}
}

func TestResetIsReportedAndStaleIdsAreRejected(t *testing.T) {
	s := New(0)
	s.Ingest("a", msg("1"))
	var cur Cursor
	s.Poll(&cur, nil, false, -1)
	old := cur.Epoch

	s.Reset()
	s.Ingest("b", msg("1"))
	if s.Select(old, 0) {
		t.Fatal("select with a stale epoch must fail")
	}
	u := s.Poll(&cur, nil, false, -1)
	if !u.Reset || len(u.Names) != 1 || u.Names[0] != "b" || u.Stats.Messages != 1 {
		t.Fatalf("update after reset = %+v", u)
	}
}

func TestMaxTopicsDropsNewTopicsOnly(t *testing.T) {
	s := New(2)
	s.Ingest("a", msg("1"))
	s.Ingest("b", msg("1"))
	s.Ingest("c", msg("1"))
	s.Ingest("a", msg("2"))
	var cur Cursor
	u := s.Poll(&cur, nil, false, -1)
	if u.Stats.Topics != 2 || u.Stats.Dropped != 1 || u.Stats.Messages != 3 {
		t.Fatalf("stats = %+v", u.Stats)
	}
}

func TestConcurrentIngestAndPoll(t *testing.T) {
	s := New(0)
	const writers, topics, rounds = 4, 5000, 20
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				for i := 0; i < topics; i++ {
					s.Ingest(fmt.Sprintf("/topic/%d/%d", w, i), msg("v"))
				}
			}
		}(w)
	}
	done := make(chan struct{})
	counts := map[uint32]uint32{}
	var cur Cursor
	drain := func() {
		for {
			u := s.Poll(&cur, nil, false, -1)
			for id, c := range countsMap(u.Counts) {
				counts[id] = c
			}
			if !u.More {
				return
			}
		}
	}
	go func() {
		defer close(done)
		for {
			drain()
			if s.Stats().Messages == writers*topics*rounds {
				drain()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
	<-done

	if len(counts) != writers*topics {
		t.Fatalf("reader saw %d topics, want %d", len(counts), writers*topics)
	}
	for id, c := range counts {
		if c != rounds {
			t.Fatalf("topic %d count = %d, want %d", id, c, rounds)
		}
	}
}

func BenchmarkIngest40kTopics(b *testing.B) {
	s := New(0)
	names := make([]string, 40_000)
	for i := range names {
		names[i] = fmt.Sprintf("/topic/SN%08d", i)
	}
	m := msg(`{"power":1234,"soc":57}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Ingest(names[i%len(names)], m)
	}
}

func BenchmarkPoll40kTopicsAllDirty(b *testing.B) {
	s := New(0)
	names := make([]string, 40_000)
	for i := range names {
		names[i] = fmt.Sprintf("/topic/SN%08d", i)
		s.Ingest(names[i], msg("x"))
	}
	var cur Cursor
	s.Poll(&cur, nil, false, -1)
	s.Poll(&cur, nil, false, -1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cur.Seq = 0
		s.Poll(&cur, nil, false, -1)
	}
}
