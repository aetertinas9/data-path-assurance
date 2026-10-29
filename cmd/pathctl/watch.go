package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/datapath"
)

// runWatch re-reads the host on an interval and prints a line whenever a
// check changes state, so that a burn-in or a cable reseat can be watched
// without reading the full report each time. It exits 3 when any check was
// FAIL at any point, which is what a burn-in wants to know.
func runWatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.flags(fs)
	interval := fs.Duration("interval", 15*time.Second, "time between reads")
	count := fs.Int("count", 0, "stop after this many reads (0: until interrupted)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *interval <= 0 || *count < 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	opts, code := c.options(stderr)
	if code != 0 {
		return code
	}

	prev := map[string]datapath.Check{}
	failed := false
reads:
	for n := 1; *count == 0 || n <= *count; n++ {
		report, err := collect(ctx, c, opts)
		if err != nil {
			fmt.Fprintf(stderr, "pathctl: %v\n", err)
			return 1
		}
		if c.gpu != "" {
			g := findGPU(report, c.gpu)
			if g == nil {
				fmt.Fprintf(stderr, "pathctl: GPU %q not found\n", c.gpu)
				return 4
			}
			report.GPUs = []*datapath.GPU{g}
		}
		stamp := time.Now().UTC().Format(time.RFC3339)
		overall := report.Overall()
		if overall == datapath.Fail {
			failed = true
		}
		fmt.Fprintf(stdout, "%s read %d result=%s gpus=%d\n", stamp, n, overall, len(report.GPUs))
		for _, g := range report.GPUs {
			for _, ch := range g.Checks {
				key := g.Dev.BDF + " " + ch.Name
				old, seen := prev[key]
				switch {
				case !seen:
					fmt.Fprintf(stdout, "  %s %s %s  %s\n", g.Dev.BDF, ch.Name, ch.Status, ch.Detail)
				case old.Status != ch.Status || old.Detail != ch.Detail:
					fmt.Fprintf(stdout, "  %s %s %s -> %s  %s\n", g.Dev.BDF, ch.Name, old.Status, ch.Status, ch.Detail)
				}
				prev[key] = ch
			}
		}
		if *count != 0 && n == *count {
			break
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(stdout, "stopped\n")
			break reads
		case <-time.After(*interval):
		}
	}
	if failed {
		return 3
	}
	return 0
}
