package runner

import (
	"context"
	"log"
	"sync"
	"time"
)

// PoolConfig sizes a worker's container pool.
type PoolConfig struct {
	Size           int    // ready containers to keep warm
	Memory         string // per container, e.g. "1536m"
	CPUs           string // per container, e.g. "1"
	ExecTimeoutSec int    // n8n's own per-execution cap
}

// Pool keeps Size fresh judge containers booted. Each job takes one; it is destroyed afterwards and
// a new one boots in its place, so no run ever sees another run's state.
type Pool struct {
	cfg   PoolConfig
	env   *Env
	ready chan *container
	wake  chan struct{}
	mu    sync.Mutex
	alive int // booting + ready + in use
}

// NewPool checks the judge image exists and starts filling the pool in the background.
func NewPool(ctx context.Context, cfg PoolConfig) (*Pool, error) {
	key, err := imageAPIKey(ctx, JudgeImage)
	if err != nil {
		return nil, err
	}
	if cfg.Size < 1 {
		cfg.Size = 1
	}
	cleanupStale(ctx)
	admin, err := ensureMock(ctx)
	if err != nil {
		return nil, err
	}
	p := &Pool{cfg: cfg, env: &Env{APIKey: key, MockAdmin: admin}, ready: make(chan *container, cfg.Size), wake: make(chan struct{}, 1)}
	go p.fill(ctx)
	p.poke()
	return p, nil
}

// Cleanup removes every judge container on this machine. Call it on shutdown.
func Cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cleanupStale(ctx)
}

// cleanupStale removes judge containers left behind by a previous crash of this worker.
func cleanupStale(ctx context.Context) {
	ids, err := docker(ctx, "ps", "-aq", "--filter", "label=meow.judge=1")
	if err != nil || ids == "" {
		return
	}
	for _, id := range splitLines(ids) {
		_, _ = docker(ctx, "rm", "-f", id)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func (p *Pool) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Pool) fill(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-time.After(5 * time.Second):
		}
		for {
			p.mu.Lock()
			if p.alive >= p.cfg.Size {
				p.mu.Unlock()
				break
			}
			p.alive++
			p.mu.Unlock()
			go p.boot(ctx)
		}
	}
}

func (p *Pool) boot(ctx context.Context) {
	t0 := time.Now()
	c, err := startContainer(ctx, runOpts{image: JudgeImage, execTimeoutSec: p.cfg.ExecTimeoutSec, memory: p.cfg.Memory, cpus: p.cfg.CPUs, withMock: true})
	if err != nil {
		log.Printf("pool: container failed to start: %v", err)
		p.mu.Lock()
		p.alive--
		p.mu.Unlock()
		time.Sleep(3 * time.Second)
		p.poke()
		return
	}
	log.Printf("pool: %s ready in %.1fs", c.Name, time.Since(t0).Seconds())
	p.ready <- c
}

// Run takes a ready container, executes the job in it, and destroys it.
func (p *Pool) Run(ctx context.Context, job *Job) *Result {
	var c *container
	select {
	case c = <-p.ready:
	case <-ctx.Done():
		return &Result{RunID: job.RunID, Infra: "worker shutting down"}
	}
	defer func() {
		go func() {
			c.destroy()
			p.mu.Lock()
			p.alive--
			p.mu.Unlock()
			p.poke()
		}()
	}()
	return Execute(ctx, c, p.env, job)
}

// Size is the configured pool size.
func (p *Pool) Size() int { return p.cfg.Size }
