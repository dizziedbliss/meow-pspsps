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
	"regexp"
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

// providerConfig is one grader provider in the JSON file named by JUDGE_LLM_CONFIG.
type providerConfig struct {
	Name     string `json:"name"`
	Base     string `json:"base"`  // OpenAI-compatible base URL, e.g. https://generativelanguage.googleapis.com/v1beta/openai
	Model    string `json:"model"` // exact model id
	Key      string `json:"key"`   // or key_file; empty is fine for a local Ollama
	KeyFile  string `json:"key_file"`
	KeysFile string `json:"keys_file"` // many keys, one per line (# comments allowed): one provider per key, used one after the other
	GapMs    int    `json:"gap_ms"`    // minimum time between calls (rate limit)
	TimeoutS int    `json:"timeout_s"` // per call, default 60
	Attempts int    `json:"attempts"`  // tries per call before failing over, default 2 when chained
}

// judgeFromEnv builds the rubric grader. Providers are always explicit; nothing is guessed from a key.
//   - JUDGE_LLM_CONFIG=file.json: a JSON list of providers, tried in order (failover).
//   - or one provider from JUDGE_LLM_BASE + JUDGE_LLM_MODEL + JUDGE_LLM_KEY / JUDGE_LLM_KEY_FILE.
func judgeFromEnv() grade.Judge {
	var cfgs []providerConfig
	if f := os.Getenv("JUDGE_LLM_CONFIG"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Printf("grader: cannot read %s: %v", f, err)
			return nil
		}
		if err := json.Unmarshal(b, &cfgs); err != nil {
			log.Printf("grader: bad %s: %v", f, err)
			return nil
		}
	} else {
		cfgs = []providerConfig{{Name: "env", Base: os.Getenv("JUDGE_LLM_BASE"), Model: os.Getenv("JUDGE_LLM_MODEL"),
			Key: os.Getenv("JUDGE_LLM_KEY"), KeyFile: os.Getenv("JUDGE_LLM_KEY_FILE")}}
		if cfgs[0].Key == "" && cfgs[0].KeyFile == "" {
			return nil // nothing configured: rubric tests are skipped (and fail as judge errors)
		}
	}
	// keys_file expands into one provider per key (each key has its own rate limit).
	var expanded []providerConfig
	for _, c := range cfgs {
		if c.KeysFile == "" {
			expanded = append(expanded, c)
			continue
		}
		path := c.KeysFile
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = home + path[1:]
		}
		b, err := os.ReadFile(path)
		if err != nil {
			log.Printf("grader: provider %q: cannot read keys file: %v", c.Name, err)
			continue
		}
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			n++
			k := c
			k.KeysFile, k.KeyFile, k.Key = "", "", line
			k.Name = fmt.Sprintf("%s-%d", c.Name, n)
			expanded = append(expanded, k)
		}
		log.Printf("grader: %s: %d keys from %s", c.Name, n, c.KeysFile)
	}
	cfgs = expanded
	var ps []*grade.OpenAIJudge
	for _, c := range cfgs {
		key := strings.TrimSpace(c.Key)
		if key == "" && c.KeyFile != "" {
			path := c.KeyFile
			if strings.HasPrefix(path, "~/") {
				home, _ := os.UserHomeDir()
				path = home + path[1:]
			}
			if b, err := os.ReadFile(path); err == nil {
				key = strings.TrimSpace(string(b))
			} else {
				log.Printf("grader: provider %q: cannot read key file: %v", c.Name, err)
				continue
			}
		}
		attempts := c.Attempts
		if attempts == 0 && len(cfgs) > 1 {
			attempts = 2
		}
		p, err := grade.NewProvider(c.Name, c.Base, key, c.Model, time.Duration(c.GapMs)*time.Millisecond, time.Duration(c.TimeoutS)*time.Second, attempts)
		if err != nil {
			log.Printf("grader: %v", err)
			continue
		}
		ps = append(ps, p)
	}
	ch, err := grade.NewChain(ps...)
	if err != nil {
		return nil
	}
	return ch
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
	count := fs.Int("count", 0, "how many teams (default: one per name in --names, else 100)")
	prefix := fs.String("prefix", "T", "team code prefix")
	start := fs.Int("start", 1, "first team number")
	names := fs.String("names", "", "file with one team name per line (in order); teams get random 10-character passwords")
	out := fs.String("out", "teams.csv", "where to write code,name,password")
	_ = fs.Parse(args)

	st, err := server.OpenStore(*db)
	if err != nil {
		return err
	}
	var nameList, codeList []string
	if *names != "" {
		raw, err := os.ReadFile(*names)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(strings.Trim(line, "\r")); line != "" {
				code, name := splitTeamLine(line)
				codeList = append(codeList, code)
				nameList = append(nameList, name)
			}
		}
	}
	if *count == 0 {
		*count = 100
		if len(nameList) > 0 {
			*count = len(nameList)
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
		if i < len(codeList) && codeList[i] != "" {
			code = codeList[i] // the names file gave this team its own code (e.g. AA07)
		}
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

var teamCodeRe = regexp.MustCompile(`^[A-Za-z]+[0-9]+$`)

// splitTeamLine reads "AA07<tab>Guardians of git" (tab or two or more spaces) as code + name; a
// line with no such code is just a name and gets a generated code.
func splitTeamLine(line string) (code, name string) {
	if i := strings.IndexAny(line, "\t"); i > 0 {
		if c := strings.TrimSpace(line[:i]); teamCodeRe.MatchString(c) {
			return strings.ToUpper(c), strings.TrimSpace(line[i+1:])
		}
	}
	if f := strings.Fields(line); len(f) > 1 && teamCodeRe.MatchString(f[0]) && strings.Contains(line, f[0]+"  ") {
		return strings.ToUpper(f[0]), strings.TrimSpace(strings.TrimPrefix(line, f[0]))
	}
	return "", line
}
