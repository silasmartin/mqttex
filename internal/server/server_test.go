package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/silasmartin/mqttex/internal/broker"
	"github.com/silasmartin/mqttex/internal/profiles"
	"github.com/silasmartin/mqttex/internal/store"
	"github.com/silasmartin/mqttex/internal/testcert"
)

// aclHook lets everybody in but refuses any topic below denied/.
type aclHook struct{ mqtt.HookBase }

func (h *aclHook) ID() string { return "test-acl" }
func (h *aclHook) Provides(b byte) bool {
	return b == mqtt.OnConnectAuthenticate || b == mqtt.OnACLCheck
}
func (h *aclHook) OnConnectAuthenticate(*mqtt.Client, packets.Packet) bool { return true }
func (h *aclHook) OnACLCheck(_ *mqtt.Client, topic string, _ bool) bool {
	return !strings.HasPrefix(topic, "denied/")
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startBroker(t *testing.T) (*mqtt.Server, int) {
	t.Helper()
	port := freePort(t)
	srv := mqtt.New(&mqtt.Options{InlineClient: true})
	if err := srv.AddHook(new(aclHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: fmt.Sprintf("127.0.0.1:%d", port)})); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return srv, port
}

type env struct {
	t      *testing.T
	st     *store.Store
	br     *broker.Manager
	book   *profiles.Book
	http   *httptest.Server
	client *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	book, err := profiles.Open(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(0)
	br := broker.New(st)
	ui := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>ui")}}
	ts := httptest.NewServer(New(st, br, book, ui, true))
	t.Cleanup(func() {
		br.Disconnect()
		ts.Close()
	})
	return &env{t: t, st: st, br: br, book: book, http: ts, client: ts.Client()}
}

func (e *env) do(method, path string, body any, mutate func(*http.Request)) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, e.http.URL+path, &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// wsSession collects what a browser would know after reading the socket.
type wsSession struct {
	t        *testing.T
	conn     *websocket.Conn
	names    map[uint32]string
	counts   map[uint32]uint32
	previews map[uint32]wirePreview
	compare  map[uint32]wirePreview
	history  []wireMessage
	last     wireTick
}

func (e *env) dialWS() *wsSession {
	e.t.Helper()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(e.http.URL, "http")+"/ws", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	conn.SetReadLimit(64 << 20)
	e.t.Cleanup(func() { conn.CloseNow() })
	return &wsSession{t: e.t, conn: conn, names: map[uint32]string{}, counts: map[uint32]uint32{}, previews: map[uint32]wirePreview{}, compare: map[uint32]wirePreview{}}
}

func (s *wsSession) send(m clientMsg) {
	s.t.Helper()
	data, _ := json.Marshal(m)
	if err := s.conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		s.t.Fatal(err)
	}
}

func (s *wsSession) readUntil(what string, cond func() bool) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for !cond() {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			s.t.Fatalf("waiting for %s: %v", what, err)
		}
		if typ == websocket.MessageBinary {
			if binary.LittleEndian.Uint32(data) != frameCounts {
				s.t.Fatalf("unknown binary frame type %d", binary.LittleEndian.Uint32(data))
			}
			n := int(binary.LittleEndian.Uint32(data[4:]))
			if len(data) != 8+8*n {
				s.t.Fatalf("counts frame has %d bytes for %d pairs", len(data), n)
			}
			for i := 0; i < n; i++ {
				id := binary.LittleEndian.Uint32(data[8+8*i:])
				if _, known := s.names[id]; !known {
					s.t.Fatalf("count for id %d arrived before its name", id)
				}
				s.counts[id] = binary.LittleEndian.Uint32(data[12+8*i:])
			}
			continue
		}
		var tick wireTick
		if err := json.Unmarshal(data, &tick); err != nil {
			s.t.Fatal(err)
		}
		if tick.Reset {
			s.names, s.counts = map[uint32]string{}, map[uint32]uint32{}
		}
		for i, name := range tick.Names {
			s.names[tick.First+uint32(i)] = name
		}
		for _, p := range tick.Previews {
			s.previews[p.ID] = p
		}
		for _, p := range tick.Compare {
			s.compare[p.ID] = p
		}
		s.history = append(s.history, tick.History...)
		s.last = tick
	}
}

func TestEndToEndBrokerToBrowser(t *testing.T) {
	mq, port := startBroker(t)
	e := newEnv(t)
	p, err := e.book.Save(profiles.Profile{Name: "local", Protocol: "mqtt", Host: "127.0.0.1", Port: port,
		Subscriptions: []string{"/topic/#", "denied/#"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	if code, body := e.do("POST", "/api/connect", map[string]string{"id": p.ID}, nil); code != 200 {
		t.Fatalf("connect = %d %v", code, body)
	}
	e.waitFor("the broker connection", func() bool {
		s := e.br.Status()
		return s.State == broker.StateConnected && len(s.SubErrors) > 0
	})
	if errs := e.br.Status().SubErrors; len(errs) != 1 || !strings.Contains(errs[0], "denied/#") || !strings.Contains(errs[0], "not authorized") {
		t.Fatalf("rejected subscription was not reported: %v", errs)
	}

	const topics = 25_000 // more than one names frame
	for round := 1; round <= 2; round++ {
		for i := 0; i < topics; i++ {
			payload := fmt.Sprintf(`{"round":%d}`, round)
			if err := mq.Publish(fmt.Sprintf("/topic/SN%06d", i), []byte(payload), false, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	mq.Publish("elsewhere/x", []byte("not subscribed"), false, 0)

	ws := e.dialWS()
	ws.readUntil("all topics and counts", func() bool {
		if len(ws.names) != topics || len(ws.counts) != topics {
			return false
		}
		for _, c := range ws.counts {
			if c != 2 {
				return false
			}
		}
		return true
	})
	if ws.last.Stats.Messages != 2*topics || ws.last.Status.State != broker.StateConnected {
		t.Fatalf("stats = %+v status = %+v", ws.last.Stats, ws.last.Status)
	}
	var id uint32
	for i, name := range ws.names {
		if strings.HasPrefix(name, "elsewhere") {
			t.Fatalf("received unsubscribed topic %s", name)
		}
		if name == "/topic/SN000042" {
			id = i
		}
	}

	// Values are only sent for rows the browser says it shows.
	if len(ws.previews) != 0 {
		t.Fatalf("got %d previews without watching anything", len(ws.previews))
	}
	ws.send(clientMsg{Type: "watch", Epoch: ws.last.Epoch, IDs: []uint32{id}})
	ws.readUntil("the watched preview", func() bool { return len(ws.previews) == 1 })
	if pv := ws.previews[id]; pv.Text == nil || *pv.Text != `{"round":2}` {
		t.Fatalf("preview = %+v", pv)
	}

	// The compare channel carries whole payloads, unlike the 160-byte previews.
	long := `{"blob":"` + strings.Repeat("x", 300) + `"}`
	if err := mq.Publish("/topic/LONG", []byte(long), false, 0); err != nil {
		t.Fatal(err)
	}
	ws.readUntil("the long topic", func() bool { return len(ws.names) == topics+1 })
	var longID uint32
	for i, name := range ws.names {
		if name == "/topic/LONG" {
			longID = i
		}
	}
	ws.send(clientMsg{Type: "compare", Epoch: ws.last.Epoch, IDs: []uint32{longID, id}})
	ws.readUntil("the compared messages", func() bool { return len(ws.compare) == 2 })
	if cv := ws.compare[longID]; cv.Text == nil || *cv.Text != long || cv.Trunc || cv.Size != len(long) {
		t.Fatalf("compared = %+v", cv)
	}
	if cv := ws.compare[id]; cv.Text == nil || *cv.Text != `{"round":2}` {
		t.Fatalf("compared = %+v", cv)
	}
	if len(ws.previews) != 1 {
		t.Fatalf("compare leaked into previews: %d", len(ws.previews))
	}

	// Selecting a topic starts its history with the latest message; publishing
	// through the API comes back as the next entry.
	ws.send(clientMsg{Type: "select", Epoch: ws.last.Epoch, ID: int64(id)})
	ws.readUntil("the history seed", func() bool { return len(ws.history) == 1 })
	if ws.last.Selected != int64(id) || ws.history[0].N != 2 {
		t.Fatalf("selected = %d history = %+v", ws.last.Selected, ws.history)
	}
	req := broker.PublishRequest{Topic: "/topic/SN000042", Payload: "from the ui", QoS: 1}
	if code, body := e.do("POST", "/api/publish", req, nil); code != 200 {
		t.Fatalf("publish = %d %v", code, body)
	}
	// The counts frame of an update follows its JSON frame.
	ws.readUntil("the published message", func() bool { return len(ws.history) == 2 && ws.counts[id] == 3 })
	if m := ws.history[1]; m.N != 3 || m.Text == nil || *m.Text != "from the ui" {
		t.Fatalf("history entry = %+v", m)
	}
	if ws.counts[id] != 3 {
		t.Fatalf("count = %d, want 3", ws.counts[id])
	}

	// Clearing resets every browser.
	if code, _ := e.do("POST", "/api/clear", nil, nil); code != 200 {
		t.Fatalf("clear = %d", code)
	}
	ws.readUntil("the reset", func() bool { return ws.last.Reset })
	if len(ws.names) != 0 || ws.last.Selected != -1 {
		t.Fatalf("after reset: %d names, selected %d", len(ws.names), ws.last.Selected)
	}

	if code, body := e.do("POST", "/api/publish", broker.PublishRequest{Topic: "a/#"}, nil); code != 400 {
		t.Fatalf("publish to wildcard topic = %d %v", code, body)
	}
	e.do("POST", "/api/disconnect", nil, nil)
	if code, body := e.do("POST", "/api/publish", req, nil); code != 400 || !strings.Contains(fmt.Sprint(body["error"]), "not connected") {
		t.Fatalf("publish while disconnected = %d %v", code, body)
	}
}

// startTLSBroker serves MQTT over TLS and WebSocket over TLS with a
// certificate for 127.0.0.1 signed by ca.
func startTLSBroker(t *testing.T, ca *testcert.CA) (mqttsPort, wssPort int) {
	t.Helper()
	mqttsPort, wssPort = freePort(t), freePort(t)
	srv := mqtt.New(&mqtt.Options{InlineClient: true})
	if err := srv.AddHook(new(aclHook), nil); err != nil {
		t.Fatal(err)
	}
	cfg := ca.ServerTLS(t)
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "tls", Address: fmt.Sprintf("127.0.0.1:%d", mqttsPort), TLSConfig: cfg})); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddListener(listeners.NewWebsocket(listeners.Config{ID: "wss", Address: fmt.Sprintf("127.0.0.1:%d", wssPort), TLSConfig: cfg})); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return mqttsPort, wssPort
}

func TestCustomCACert(t *testing.T) {
	ca := testcert.NewCA(t, "Test Root CA", time.Now().Add(time.Hour))
	other := testcert.NewCA(t, "Other CA", time.Now().Add(time.Hour))
	mqttsPort, wssPort := startTLSBroker(t, ca)
	e := newEnv(t)

	connect := func(protocol string, port int, caCert string) broker.Status {
		t.Helper()
		code, saved := e.do("POST", "/api/profiles", map[string]any{"protocol": protocol, "host": "127.0.0.1", "port": port, "caCert": caCert}, nil)
		if code != 200 {
			t.Fatalf("save = %d %v", code, saved)
		}
		if code, body := e.do("POST", "/api/connect", map[string]any{"id": saved["id"]}, nil); code != 200 {
			t.Fatalf("connect = %d %v", code, body)
		}
		e.waitFor(protocol+" connection result", func() bool {
			s := e.br.Status()
			return s.State == broker.StateConnected || s.Error != ""
		})
		return e.br.Status()
	}

	// The test CA is not among the system roots.
	if s := connect("mqtts", mqttsPort, ""); s.State == broker.StateConnected || !strings.Contains(s.Error, "add its certificate to the connection") {
		t.Errorf("without ca: %+v", s)
	}
	if s := connect("wss", wssPort, ""); s.State == broker.StateConnected || !strings.Contains(s.Error, "add its certificate to the connection") {
		t.Errorf("wss without ca: %+v", s)
	}
	if s := connect("mqtts", mqttsPort, other.PEM); s.State == broker.StateConnected || !strings.Contains(s.Error, "not signed by the CA certificate of this connection") {
		t.Errorf("with another ca: %+v", s)
	}
	if s := connect("mqtts", mqttsPort, ca.PEM); s.State != broker.StateConnected {
		t.Errorf("mqtts with the ca: %+v", s)
	}
	if s := connect("wss", wssPort, ca.PEM); s.State != broker.StateConnected {
		t.Errorf("wss with the ca: %+v", s)
	}

	code, body := e.do("POST", "/api/profiles", map[string]any{"protocol": "mqtts", "host": "h", "port": 8883, "caCert": "not a certificate"}, nil)
	if code != 400 || !strings.Contains(fmt.Sprint(body["error"]), "CA certificate") {
		t.Errorf("save with a broken ca = %d %v", code, body)
	}

	res, err := e.client.Get(e.http.URL + "/api/profiles")
	if err != nil {
		t.Fatal(err)
	}
	var list []profiles.Public
	json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	if len(list) != 5 || len(list[3].CACerts) != 1 || list[3].CACerts[0].Subject != "Test Root CA" || list[3].CACert != strings.TrimSpace(ca.PEM) {
		t.Fatalf("profiles = %+v", list)
	}
}

func TestConnectErrorIsReported(t *testing.T) {
	e := newEnv(t)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // nothing listens here any more

	p, _ := e.book.Save(profiles.Profile{Protocol: "mqtt", Host: "127.0.0.1", Port: port}, false)
	if code, _ := e.do("POST", "/api/connect", map[string]string{"id": p.ID}, nil); code != 200 {
		t.Fatalf("connect = %d", code)
	}
	e.waitFor("the connect error", func() bool { return e.br.Status().Error != "" })
	if s := e.br.Status(); s.State != broker.StateConnecting {
		t.Fatalf("status = %+v", s)
	}
	if code, _ := e.do("POST", "/api/connect", map[string]string{"id": "nope"}, nil); code != 404 {
		t.Fatalf("connect with unknown profile = %d", code)
	}
}

func TestProfilesAPINeverReturnsPasswords(t *testing.T) {
	e := newEnv(t)
	code, saved := e.do("POST", "/api/profiles", map[string]any{"protocol": "mqtts", "host": "broker.example", "port": 8883,
		"username": "u", "password": "hunter2", "subscriptions": []string{"/topic/#"}}, nil)
	if code != 200 || saved["hasPassword"] != true || saved["password"] != nil {
		t.Fatalf("save = %d %v", code, saved)
	}

	res, err := e.client.Get(e.http.URL + "/api/profiles")
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	raw.ReadFrom(res.Body)
	res.Body.Close()
	if strings.Contains(raw.String(), "hunter2") || !strings.Contains(raw.String(), "broker.example") {
		t.Fatalf("profiles = %s", raw.String())
	}
	if stored, _ := e.book.Get(saved["id"].(string)); stored.Password != "hunter2" {
		t.Fatalf("stored password = %q", stored.Password)
	}

	if code, body := e.do("POST", "/api/profiles", map[string]any{"protocol": "mqtt", "host": "h", "port": 1883,
		"subscriptions": []string{"a/#/b"}}, nil); code != 400 || !strings.Contains(fmt.Sprint(body["error"]), "last level") {
		t.Fatalf("bad filter = %d %v", code, body)
	}
	if code, _ := e.do("DELETE", "/api/profiles/"+saved["id"].(string), nil, nil); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := e.do("DELETE", "/api/profiles/"+saved["id"].(string), nil, nil); code != 404 {
		t.Fatalf("second delete = %d", code)
	}
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	e := newEnv(t)
	cases := map[string]func(*http.Request){
		"foreign origin":   func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"form content":     func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"rebound hostname": func(r *http.Request) { r.Host = "evil.example" },
	}
	for name, mutate := range cases {
		if code, body := e.do("POST", "/api/clear", nil, mutate); code != http.StatusForbidden {
			t.Errorf("%s: status = %d %v, want 403", name, code, body)
		}
	}
	sameOrigin := func(r *http.Request) { r.Header.Set("Origin", e.http.URL) }
	if code, body := e.do("POST", "/api/clear", nil, sameOrigin); code != 200 {
		t.Errorf("same origin: status = %d %v", code, body)
	}

	_, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(e.http.URL, "http")+"/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	if err == nil {
		t.Error("websocket from a foreign origin was accepted")
	}

	res, err := e.client.Get(e.http.URL + "/")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("ui: %v %v", res, err)
	}
	res.Body.Close()
}

func TestEncodePayload(t *testing.T) {
	if w := encodePayload([]byte("hello"), 10); w.Text == nil || *w.Text != "hello" || w.Trunc || w.Size != 5 {
		t.Errorf("text = %+v", w)
	}
	// The cut must not split the two-byte "ä".
	if w := encodePayload([]byte("aä"), 2); w.Text == nil || *w.Text != "a" || !w.Trunc || w.Size != 3 {
		t.Errorf("truncated = %+v", w)
	}
	if w := encodePayload([]byte{0xff, 0xfe}, 10); w.Text != nil || w.Base64 != "//4=" {
		t.Errorf("binary = %+v", w)
	}
	if w := encodePayload(nil, 10); w.Text == nil || *w.Text != "" || w.Size != 0 {
		t.Errorf("empty = %+v", w)
	}
}
