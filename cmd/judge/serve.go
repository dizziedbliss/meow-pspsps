package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/dizziedbliss/meow-pspsps/internal/grade"
	"github.com/dizziedbliss/meow-pspsps/internal/runner"
	"github.com/dizziedbliss/meow-pspsps/internal/server"
)

// judgeFromEnv builds the rubric grader from JUDGE_LLM_KEY (or JUDGE_LLM_KEY_FILE), JUDGE_LLM_BASE
// and JUDGE_LLM_MODEL. A Google "AIza…" key defaults to Gemini's OpenAI-compatible endpoint.
func judgeFromEnv() grade.Judge {
	key := strings.TrimSpace(os.Getenv("JUDGE_LLM_KEY"))
	if key == "" {
		if f := os.Getenv("JUDGE_LLM_KEY_FILE"); f != "" {
			if b, err := os.ReadFile(f); err == nil {
				key = strings.TrimSpace(string(b))
			}
		}
	}
	base, model := os.Getenv("JUDGE_LLM_BASE"), os.Getenv("JUDGE_LLM_MODEL")
	if strings.HasPrefix(key, "AIza") {
		if base == "" {
			base = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		if model == "" {
			model = "gemini-2.5-flash"
		}
	}
	// Never guess where a key belongs: unknown key formats require an explicit endpoint.
	if key != "" && base == "" {
		return nil
	}
	if j := grade.NewOpenAIJudge(base, key, model); j != nil {
		return j
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", env("JUDGE_ADDR", "127.0.0.1:8080"), "listen address (cloudflared points here)")
	db := fs.String("db", env("JUDGE_DB", "judge.db"), "SQLite file")
	probs := fs.String("problems", env("JUDGE_PROBLEMS", "problems"), "problems directory")
	rounds := fs.String("rounds", env("JUDGE_ROUNDS", ""), "round→problem map, e.g. 1=p1,2=p2,3=p3")
	local := fs.Int("local-pool", 0, "also run tests on this machine with a pool of N containers")
	_ = fs.Parse(args)

	cfg := server.Config{
		DBPath: *db, ProblemsDir: *probs, Rounds: map[int]string{},
		AdminPassword: os.Getenv("JUDGE_ADMIN_PASSWORD"),
		WorkerToken:   os.Getenv("JUDGE_WORKER_TOKEN"),
	}
	if cfg.AdminPassword == "" || cfg.WorkerToken == "" {
		return errors.New("set JUDGE_ADMIN_PASSWORD and JUDGE_WORKER_TOKEN")
	}
	if j := judgeFromEnv(); j != nil {
		cfg.Judge = j
	} else {
		log.Printf("warning: rubric judge not configured; rubric tests cannot be scored")
	}
	for _, part := range strings.Split(*rounds, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		n, err := strconv.Atoi(k)
		if !ok || err != nil {
			return fmt.Errorf("bad --rounds entry %q", part)
		}
		cfg.Rounds[n] = v
	}
	if len(cfg.Rounds) == 0 {
		return errors.New("--rounds is required, e.g. --rounds 1=dummy")
	}
	srv, err := server.New(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.Start(ctx)
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	if *local > 0 {
		go func() {
			if err := runWorker(ctx, "http://"+*addr, cfg.WorkerToken, hostname()+"-local", *local); err != nil && ctx.Err() == nil {
				log.Printf("local worker stopped: %v", err)
			}
		}()
	}
	log.Printf("judge listening on http://%s (rounds %v)", *addr, cfg.Rounds)
	if *local > 0 {
		defer runner.Cleanup()
	}
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "worker"
	}
	return h
}

func cmdWorker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	central := fs.String("central", env("JUDGE_CENTRAL", ""), "central site URL, e.g. https://judge.example.com")
	tok := fs.String("token", env("JUDGE_WORKER_TOKEN", ""), "worker token")
	pool := fs.Int("pool", 2, "n8n containers to run at once (about 1 GB RAM each)")
	name := fs.String("name", hostname(), "name shown to organisers")
	_ = fs.Parse(args)
	if *central == "" || *tok == "" {
		return errors.New("--central and --token are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer runner.Cleanup()
	return runWorker(ctx, strings.TrimRight(*central, "/"), *tok, *name, *pool)
}

// runWorker keeps `size` jobs in flight: each slot long-polls the central site, runs the job in a
// fresh container and posts the raw result back.
func runWorker(ctx context.Context, central, tok, name string, size int) error {
	pool, err := runner.NewPool(ctx, runner.PoolConfig{Size: size, Memory: "1536m", CPUs: "1", ExecTimeoutSec: 90})
	if err != nil {
		return err
	}
	log.Printf("worker %s: pool of %d, pulling from %s", name, size, central)
	hc := &http.Client{Timeout: 40 * time.Second}
	var wg sync.WaitGroup
	for i := 0; i < size; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			backoff := time.Second
			for ctx.Err() == nil {
				job, err := claim(ctx, hc, central, tok, fmt.Sprintf("%s#%d", name, slot))
				if err != nil {
					log.Printf("worker: claim failed: %v (retrying in %s)", err, backoff)
					select {
					case <-time.After(backoff):
					case <-ctx.Done():
					}
					if backoff < 30*time.Second {
						backoff *= 2
					}
					continue
				}
				backoff = time.Second
				if job == nil {
					continue
				}
				log.Printf("worker: run %d (%d tests)", job.RunID, len(job.Tests))
				res := pool.Run(ctx, job)
				res.Worker = name
				job.Creds = nil // drop keys as soon as the container is gone
				if err := report(ctx, central, tok, res); err != nil {
					log.Printf("worker: report run %d failed: %v", job.RunID, err)
				}
			}
		}(i)
	}
	wg.Wait()
	return nil
}

func claim(ctx context.Context, hc *http.Client, central, tok, name string) (*runner.Job, error) {
	body, _ := json.Marshal(map[string]string{"worker": name})
	req, _ := http.NewRequestWithContext(ctx, "POST", central+"/api/worker/claim", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%d %s", resp.StatusCode, b)
	}
	var job runner.Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func report(ctx context.Context, central, tok string, res *runner.Result) error {
	body, _ := json.Marshal(res)
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", central+"/api/worker/result", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusConflict {
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		last = err
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return last
}

// cmdTeams creates team logins and writes them to a CSV for printing.
func cmdTeams(args []string) error {
	fs := flag.NewFlagSet("teams", flag.ExitOnError)
	db := fs.String("db", env("JUDGE_DB", "judge.db"), "SQLite file")
	count := fs.Int("count", 100, "how many teams")
	prefix := fs.String("prefix", "T", "team code prefix")
	start := fs.Int("start", 1, "first team number")
	names := fs.String("names", "", "optional CSV with one team name per line (in order)")
	out := fs.String("out", "teams.csv", "where to write code,name,password")
	_ = fs.Parse(args)

	st, err := server.OpenStore(*db)
	if err != nil {
		return err
	}
	var nameList []string
	if *names != "" {
		f, err := os.Open(*names)
		if err != nil {
			return err
		}
		recs, err := csv.NewReader(f).ReadAll()
		f.Close()
		if err != nil {
			return err
		}
		for _, r := range recs {
			if len(r) > 0 && strings.TrimSpace(r[0]) != "" {
				nameList = append(nameList, strings.TrimSpace(r[0]))
			}
		}
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%v (won't overwrite an existing file of passwords)", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"code", "name", "password"})
	for i := 0; i < *count; i++ {
		code := fmt.Sprintf("%s%03d", *prefix, *start+i)
		name := "Team " + code
		if i < len(nameList) {
			name = nameList[i]
		}
		pw := password()
		hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err := st.AddTeam(code, name, string(hash)); err != nil {
			return fmt.Errorf("team %s: %w", code, err)
		}
		_ = w.Write([]string{code, name, pw})
	}
	w.Flush()
	log.Printf("created %d teams; logins in %s (keep it private)", *count, *out)
	return w.Error()
}

// password is 10 characters without look-alikes (no 0/O, 1/l/I).
func password() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzACDEFGHJKLMNPQRTUVWXY2346789"
	b := make([]byte, 10)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}
