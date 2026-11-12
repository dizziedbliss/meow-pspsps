package grade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Chain grades with the first provider that answers and falls over to the next on errors (rate
// limits, 5xx, timeouts, unreadable JSON). Identical questions are answered from a cache, which is
// safe because the prompt is fixed and the temperature is 0. If every provider fails the error
// goes back to the caller, which treats it as a judge-side failure (attempt refunded, never a zero).
type Chain struct {
	Providers []*OpenAIJudge
	mu        sync.Mutex
	cache     map[string]cached
	start     int         // provider that answered last: tried first (sticky), so keys are used one after the other
	cooldown  []time.Time // a provider that just failed is skipped until this time (unless all are cooling)
}

type cached struct {
	ok     bool
	reason string
	info   JudgeInfo
}

// NewChain needs at least one provider.
func NewChain(p ...*OpenAIJudge) (*Chain, error) {
	if len(p) == 0 {
		return nil, errors.New("no grader providers configured")
	}
	return &Chain{Providers: p, cache: map[string]cached{}, cooldown: make([]time.Time, len(p))}, nil
}

// Grade implements Judge.
func (c *Chain) Grade(ctx context.Context, rubric, input, answer string) (bool, string, error) {
	ok, reason, _, err := c.GradeInfo(ctx, rubric, input, answer)
	return ok, reason, err
}

// GradeInfo implements Informer: it also says which provider answered, or that the cache did.
func (c *Chain) GradeInfo(ctx context.Context, rubric, input, answer string) (bool, string, JudgeInfo, error) {
	sum := sha256.Sum256([]byte(rubric + "\x00" + input + "\x00" + answer))
	key := hex.EncodeToString(sum[:])
	c.mu.Lock()
	if v, ok := c.cache[key]; ok {
		c.mu.Unlock()
		info := v.info
		info.Cached = true
		return v.ok, v.reason, info, nil
	}
	c.mu.Unlock()
	var errs []error
	n := len(c.Providers)
	c.mu.Lock()
	first := c.start
	c.mu.Unlock()
	// Two passes: first only providers that are not cooling down, then everything that is left.
	for pass := 0; pass < 2; pass++ {
		for k := 0; k < n; k++ {
			i := (first + k) % n
			p := c.Providers[i]
			c.mu.Lock()
			cooling := time.Now().Before(c.cooldown[i])
			c.mu.Unlock()
			if (pass == 0) == cooling {
				continue
			}
			ok, reason, info, err := p.GradeInfo(ctx, rubric, input, answer)
			if err == nil {
				log.Printf("grader: %s/%s answered (pass=%v)", p.Name, p.Model, ok) // which model graded what stays in the log
				c.mu.Lock()
				c.cache[key] = cached{ok: ok, reason: reason, info: info}
				c.start = i
				c.mu.Unlock()
				return ok, reason, info, nil
			}
			if ctx.Err() != nil {
				return false, "", JudgeInfo{}, ctx.Err()
			}
			log.Printf("grader: %s/%s failed: %v; trying the next provider", p.Name, p.Model, err)
			c.mu.Lock()
			c.cooldown[i] = time.Now().Add(60 * time.Second) // rate-limited or down: leave it alone for a minute
			c.mu.Unlock()
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return false, "", JudgeInfo{}, errors.Join(errs...)
}
