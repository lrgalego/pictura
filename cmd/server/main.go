// The pictura server: one binary, flag-configured, self-probing.
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

	"github.com/lrgalego/pictura/internal/jobs"
	"github.com/lrgalego/pictura/internal/meta"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/web"
)

func main() {
	host := flag.String("host", "127.0.0.1", "listen address (the container passes 0.0.0.0)")
	port := flag.Int("port", 8080, "listen port")
	dataDir := flag.String("data", "./data", "directory for the SQLite database and generated images")
	envFile := flag.String("env-file", ".env", "optional KEY=VALUE file loaded into the environment (META_API_KEY)")
	fakeAI := flag.Bool("fake-ai", false, "use the offline fake model provider even if META_API_KEY is set")
	healthCheck := flag.Bool("health-check", false, "probe /healthz on 127.0.0.1 and exit; used by the container healthcheck")
	enableUser := flag.String("enable-user", "", "enable this account and exit (signing up does not enable an account)")
	disableUser := flag.String("disable-user", "", "disable this account and exit")
	listUsers := flag.Bool("list-users", false, "print every account with its enabled flag and exit")
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

	if *enableUser != "" || *disableUser != "" || *listUsers {
		os.Exit(manageUsers(st, *enableUser, *disableUser, *listUsers))
	}
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
		delay := 1500 * time.Millisecond
		if d, err := time.ParseDuration(os.Getenv("FAKE_DELAY")); err == nil {
			delay = d
		}
		ai = &pipeline.Fake{Delay: delay}
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

// manageUsers is the operator's switch until there is an admin screen:
// run the same binary against the data dir with --enable-user <name>.
func manageUsers(st *store.Store, enable, disable string, list bool) int {
	ctx := context.Background()
	for _, op := range []struct {
		name    string
		enabled bool
	}{{enable, true}, {disable, false}} {
		if op.name == "" {
			continue
		}
		if err := st.SetUserEnabled(ctx, op.name, op.enabled); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", op.name, err)
			return 1
		}
		fmt.Printf("%s: enabled=%v\n", strings.ToLower(op.name), op.enabled)
	}
	if list {
		users, err := st.Users(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, u := range users {
			fmt.Printf("%-24s enabled=%-5v created %s\n", u.Username, u.Enabled, u.CreatedAt.Format("2006-01-02"))
		}
	}
	return 0
}

func probe(port int) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil || resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
