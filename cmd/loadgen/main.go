// loadgen starts a local MQTT broker and floods it, to try mqttex against a
// broker with tens of thousands of active topics without touching a real one.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18831", "address the broker listens on")
	topics := flag.Int("topics", 40_000, "number of device topics under /topic/")
	rate := flag.Int("rate", 20_000, "messages per second across all topics")
	flag.Parse()

	srv := mqtt.New(&mqtt.Options{InlineClient: true})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		log.Fatal(err)
	}
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "tcp", Address: *addr})); err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := srv.Serve(); err != nil {
			log.Fatal(err)
		}
	}()
	log.Printf("broker on mqtt://%s, publishing %d msg/s over %d topics", *addr, *rate, *topics)

	names := make([]string, *topics)
	for i := range names {
		names[i] = fmt.Sprintf("/topic/SN%08d", i)
	}
	const slices = 50 // publish in 20 ms slices to keep the stream smooth
	tick := time.NewTicker(time.Second / slices)
	next := 0
	for range tick.C {
		for i := 0; i < *rate/slices; i++ {
			payload := fmt.Sprintf(`{"power":%d,"soc":%d,"ts":%d}`, rand.IntN(800), rand.IntN(100), time.Now().UnixMilli())
			if err := srv.Publish(names[next], []byte(payload), false, 0); err != nil {
				log.Printf("publish: %v", err)
			}
			next = (next + 1) % len(names)
		}
	}
}
