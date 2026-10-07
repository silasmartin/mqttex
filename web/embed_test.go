package web

import (
	"io/fs"
	"regexp"
	"testing"
)

// The release binary serves only the embedded files, and one module missing
// from the embed list stops app.js from loading at all, so every file the
// page refers to has to be in FS.
func TestEmbedHoldsEverythingThePageLoads(t *testing.T) {
	refs := regexp.MustCompile(`(?:src|href)="([^":]+)"|from '\./([^']+)'`)
	seen := map[string]bool{}
	queue := []string{"index.html"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		data, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Errorf("%s is referenced by the page but not embedded", name)
			continue
		}
		for _, m := range refs.FindAllStringSubmatch(string(data), -1) {
			queue = append(queue, m[1]+m[2])
		}
	}
	for _, want := range []string{"app.js", "cert.js", "style.css"} {
		if !seen[want] {
			t.Errorf("%s was never reached; the reference pattern is out of date", want)
		}
	}
}
