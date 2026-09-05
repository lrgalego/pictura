// The story-time server: one binary, flag-configured, self-probing.
//
// --health-check exists because the production image is distroless — no shell,
// no curl — so the container healthcheck runs this same binary against itself.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lrgalego/story-time/internal/jobs"
	"github.com/lrgalego/story-time/internal/meta"
	"github.com/lrgalego/story-time/internal/pipeline"
	"github.com/lrgalego/story-time/internal/store"
	"github.com/lrgalego/story-time/web"
)

func main() {
	host := flag.String("host", "127.0.0.1", "listen address (the container passes 0.0.0.0)")
	port := flag.Int("port", 8080, "listen port")
	dataDir := flag.String("data", "./data", "directory for the SQLite database and generated images")
	envFile := flag.String("env-file", ".env", "optional KEY=VALUE file loaded into the environment (META_API_KEY)")
	fakeAI := flag.Bool("fake-ai", false, "use the offline fake model provider even if META_API_KEY is set")
	healthCheck := flag.Bool("health-check", false, "probe /healthz on 127.0.0.1 and exit; used by the container healthcheck")
	flag.Parse()

	if *healthCheck {
		os.Exit(probe(*port))
	}

	loadEnvFile(*envFile)

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.FailRunningJobs(context.Background()); err != nil {
		log.Printf("reset jobs: %v", err)
	}

	var ai pipeline.AI
	key := strings.TrimSpace(os.Getenv("META_API_KEY"))
	switch {
	case *fakeAI || key == "":
		if key == "" {
			log.Printf("META_API_KEY is not set — running with the offline fake model provider (placeholder art)")
		}
		ai = &pipeline.Fake{Delay: 1500 * time.Millisecond}
	default:
		c := meta.New(key)
		if m := os.Getenv("META_TEXT_MODEL"); m != "" {
			c.TextModel = m
		}
		if m := os.Getenv("META_IMAGE_MODEL"); m != "" {
			c.ImageModel = m
		}
		log.Printf("Meta Model API: text=%s image=%s", c.TextModel, c.ImageModel)
		ai = c
	}

	runner := jobs.New(st, ai, 3)
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", *host, *port),
		Handler:           web.Router(web.Deps{Store: st, Jobs: runner, Fake: key == "" || *fakeAI}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}

// loadEnvFile sets KEY=VALUE lines from path into the environment without
// overriding variables that are already set. Missing file = nothing.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func probe(port int) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil || resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
