package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/catalog"
	"github.com/dizziedbliss/meow-pspsps/internal/grade"
	"github.com/dizziedbliss/meow-pspsps/internal/problem"
	"github.com/dizziedbliss/meow-pspsps/internal/runner"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

func cmdBake(args []string) error {
	fs := flag.NewFlagSet("bake", flag.ExitOnError)
	out := fs.String("catalog", "", "also write the node/credential catalog here (organisers only; rebuild the binary after)")
	_ = fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := runner.Bake(ctx, func(f string, a ...any) { log.Printf(f, a...) })
	if err != nil {
		return err
	}
	log.Printf("built %s (first boot took %.1fs)", res.Image, res.BootSeconds)
	if *out != "" {
		cat, err := catalog.Build(res.NodesJSON, res.CredsJSON, runner.N8nVersion)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(cat)
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return err
		}
		log.Printf("wrote %s: %d node types, %d credential types", *out, len(cat.Nodes), len(cat.Creds))
	}
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	probDir := fs.String("problem", "", "problem directory")
	wfPath := fs.String("workflow", "", "workflow JSON file")
	times := fs.Int("times", 1, "run the whole suite this many times (checks stability)")
	verbose := fs.Bool("v", false, "print every check, input and output")
	var creds multiFlag
	fs.Var(&creds, "cred", "credential field, e.g. openAiApi.apiKey=sk-... (repeatable)")
	var credFiles multiFlag
	fs.Var(&credFiles, "cred-file", "credential field read from a file, e.g. googlePalmApi.apiKey=~/key.txt (repeatable)")
	_ = fs.Parse(args)
	if *probDir == "" || *wfPath == "" {
		return fmt.Errorf("--problem and --workflow are required")
	}
	p, err := problem.Load(*probDir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*wfPath)
	if err != nil {
		return err
	}
	cat, err := catalog.Default()
	if err != nil {
		return err
	}
	w, err := workflow.Parse(raw)
	if err != nil {
		return err
	}
	an := workflow.Analyze(w, raw, cat, p.Expect())
	for _, wn := range an.Warnings {
		fmt.Println("warning:", wn)
	}
	if len(an.Errors) > 0 {
		for _, e := range an.Errors {
			fmt.Println("INVALID:", e)
		}
		return fmt.Errorf("workflow failed the pre-check")
	}
	for _, cf := range credFiles {
		k, path, ok := strings.Cut(cf, "=")
		if !ok {
			return fmt.Errorf("bad --cred-file %q, want type.field=path", cf)
		}
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = home + path[1:]
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		creds = append(creds, k+"="+strings.TrimSpace(string(b)))
	}
	credMap := map[string]map[string]string{}
	for _, c := range creds {
		k, v, ok := strings.Cut(c, "=")
		typ, field, ok2 := strings.Cut(k, ".")
		if !ok || !ok2 {
			return fmt.Errorf("bad --cred %q, want type.field=value", c)
		}
		if credMap[typ] == nil {
			credMap[typ] = map[string]string{}
		}
		credMap[typ][field] = v
	}
	for _, n := range an.Needs {
		fmt.Printf("needs credential %s (%s) for %s\n", n.DisplayName, n.Type, strings.Join(n.Nodes, ", "))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	defer runner.Cleanup()
	pool, err := runner.NewPool(ctx, runner.PoolConfig{Size: 1, Memory: "1536m", CPUs: "1", ExecTimeoutSec: p.StepTimeoutSec + 15})
	if err != nil {
		return err
	}
	gj := judgeFromEnv()

	allGood := true
	for t := 1; t <= *times; t++ {
		seed := time.Now().UnixNano()
		rendered, err := p.Render(seed, int64(t))
		if err != nil {
			return err
		}
		job := &runner.Job{RunID: int64(t), Workflow: raw, Trigger: an.Trigger, Needs: an.Needs, Creds: credMap, StepTimout: p.StepTimeoutSec, Parallel: p.Parallel}
		for _, r := range rendered {
			job.Tests = append(job.Tests, r.Job)
		}
		res := pool.Run(ctx, job)
		if res.Invalid != "" || res.Infra != "" {
			fmt.Printf("run %d: invalid=%q infra=%q\n", t, res.Invalid, res.Infra)
			allGood = false
			continue
		}
		var passed, total float64
		fmt.Printf("\nrun %d (seed %d, %.1fs):\n", t, seed, res.Seconds)
		for i, r := range rendered {
			v := grade.Test(ctx, r, &res.Tests[i], gj)
			total += v.Weight
			mark := "✗"
			if v.Verdict == grade.Accepted {
				passed += v.Weight
				mark = "✓"
			} else {
				allGood = false
			}
			vis := "hidden"
			if v.Visible {
				vis = "sample"
			}
			fmt.Printf("  %s %-28s %-7s %-22s %-17s %5dms\n", mark, v.ID, vis, v.Category, v.Verdict, v.Ms)
			if v.Verdict != grade.Accepted || *verbose {
				for _, c := range v.Checks {
					if !c.OK || *verbose {
						fmt.Printf("      check %-40s ok=%v %s\n", c.What, c.OK, c.Detail)
					}
				}
				if v.Error != "" {
					fmt.Printf("      error: %s\n", v.Error)
				}
				if *verbose || v.Verdict != grade.Accepted {
					fmt.Printf("      input:  %s\n      actual: %s\n", oneLine(v.Input), oneLine(v.Actual))
				}
			}
		}
		fmt.Printf("  score: %.0f/%.0f\n", passed, total)
	}
	if !allGood {
		return fmt.Errorf("not every test passed")
	}
	return nil
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
