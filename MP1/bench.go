package main

import (
	"flag"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var benchPatterns = []struct {
	label string
	args  []string
}{
	{"frequent", []string{"-F", phrases["FREQ"]["ALL"]}},
	{"infrequent", []string{"-F", phrases["MID"]["ALL"]}},
	{"rare", []string{"-F", phrases["RARE"]["ALL"]}},
	{"none", []string{"-F", "this phrase is never planted in any log"}},
}

// Times each query from sending until the last VM replies
func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	trials := fs.Int("trials", 5, "measured queries per pattern and cluster size")
	vmsFlag := fs.String("vms", "", "comma-separated cluster sizes, each using the first K hosts in host.txt (default: all hosts)")
	fs.Parse(args)

	servers, err := loadServers()
	if err != nil {
		fatalf("reading VM list: %v", err)
	}
	if *trials < 2 {
		fatalf("-trials must be at least 2 to compute a standard deviation")
	}

	sizes := []int{len(servers)}
	if *vmsFlag != "" {
		sizes = nil
		for _, s := range strings.Split(*vmsFlag, ",") {
			k, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || k < 1 || k > len(servers) {
				fatalf("-vms: %q is not a cluster size between 1 and %d", s, len(servers))
			}
			sizes = append(sizes, k)
		}
	}

	fmt.Printf("%d measured trials per row\n\n", *trials)
	fmt.Printf("%-4s %-11s %10s %10s %10s %10s %10s\n", "VMs", "pattern", "matches", "mean", "stddev", "min", "max")

	for _, k := range sizes {
		for _, p := range benchPatterns {
			latencies := make([]float64, *trials) // milliseconds
			matches := 0
			for i := range latencies {
				d, m, err := measureQuery(servers[:k], p.args)
				if err != nil {
					fatalf("%s on %d VMs: %v", p.label, k, err)
				}
				latencies[i] = float64(d.Microseconds()) / 1000
				matches = m
			}

			mean, sd, lo, hi := summarize(latencies)
			fmt.Printf("%-4d %-11s %10d %8.1fms %8.1fms %8.1fms %8.1fms\n", k, p.label, matches, mean, sd, lo, hi)
		}
	}
}

// Fails if any VM fails, otherwise the time would just be the dial timeout
func measureQuery(servers []string, grepArgs []string) (time.Duration, int, error) {
	start := time.Now()
	results := queryAll(servers, grepArgs)
	elapsed := time.Since(start)

	total := 0
	for _, r := range results {
		if r.err != nil {
			return 0, 0, fmt.Errorf("%s: %v", r.addr, r.err)
		}
		total += r.matches
	}
	return elapsed, total, nil
}

func summarize(xs []float64) (mean, sd, lo, hi float64) {
	lo, hi = xs[0], xs[0]
	for _, x := range xs {
		mean += x
		lo = math.Min(lo, x)
		hi = math.Max(hi, x)
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		sd += (x - mean) * (x - mean)
	}
	sd = math.Sqrt(sd / float64(len(xs)-1))
	return mean, sd, lo, hi
}
