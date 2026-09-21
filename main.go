// mqttex is an MQTT explorer for brokers with very many topics. It keeps all
// state in memory and serves a browser UI on a local port.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"github.com/silasmartin/mqttex/internal/broker"
	"github.com/silasmartin/mqttex/internal/profiles"
	"github.com/silasmartin/mqttex/internal/server"
	"github.com/silasmartin/mqttex/internal/store"
	"github.com/silasmartin/mqttex/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18830", "address the UI is served on")
	profilesPath := flag.String("profiles", "", "connection profiles file (default: <user config dir>/mqttex/profiles.json)")
	maxTopics := flag.Int("max-topics", store.DefaultMaxTopics, "stop adding topics beyond this number")
	noOpen := flag.Bool("no-open", false, "do not open the browser")
	webDir := flag.String("web", "", "serve the UI from this directory instead of the embedded copy (development)")
	flag.Parse()

	if *profilesPath == "" {
		p, err := profiles.DefaultPath()
		if err != nil {
			log.Fatalf("cannot locate the user config directory: %v", err)
		}
		*profilesPath = p
	}
	book, err := profiles.Open(*profilesPath)
	if err != nil {
		log.Fatalf("cannot load connection profiles: %v", err)
	}

	var ui fs.FS = web.FS
	if *webDir != "" {
		ui = os.DirFS(*webDir)
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("invalid -addr %q: %v", *addr, err)
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !loopback {
		log.Printf("warning: %s is reachable from the network and mqttex has no login; anyone who can reach it can use your broker credentials", *addr)
	}

	st := store.New(*maxTopics)
	br := broker.New(st)
	defer br.Disconnect()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("cannot listen on %s: %v", *addr, err)
	}
	url := fmt.Sprintf("http://%s", ln.Addr())
	log.Printf("mqttex is running at %s (profiles: %s)", url, *profilesPath)
	if !*noOpen {
		openBrowser(url)
	}
	log.Fatal(http.Serve(ln, server.New(st, br, book, ui, loopback)))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open the browser, visit %s manually: %v", url, err)
	}
}
