// Command judge is the n8n workflow judge: central site, worker, and organiser tools in one binary.
package main

import (
	"fmt"
	"os"
)

const usage = `judge: LeetCode-style judge for n8n workflows

Usage:
  judge bake   [--catalog internal/catalog/catalog.json]   build the judge n8n image on this machine
  judge check  --problem problems/<dir> --workflow wf.json [--cred type.field=value] [--times N]
  judge serve  [flags]                                     run the central site (see judge serve -h)
  judge worker --central https://judge.example --token T [--pool N]
  judge teams  --db judge.db --count 100 [--out teams.csv] create team logins
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "bake":
		err = cmdBake(args)
	case "check":
		err = cmdCheck(args)
	case "serve":
		err = cmdServe(args)
	case "worker":
		err = cmdWorker(args)
	case "teams":
		err = cmdTeams(args)
	case "mock":
		err = cmdMock(args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
