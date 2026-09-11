package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// benchPatterns use the ALL-scope phrases so every VM greps and returns
// matches; with ONE or SOME scope most VMs would answer with nothing, and the
// latency would not reflect the whole cluster. "none" matches no line: grep
// still scans the whole file, so it is the baseline cost of a query with
// nothing to send back.
var benchPatterns = []struct {
	label string
	args  []string
}{
	{"frequent", []string{"-F", phrases["FREQ"]["ALL"]}},
	{"infrequent", []string{"-F", phrases["MID"]["ALL"]}},
	{"rare", []string{"-F", phrases["RARE"]["ALL"]}},
	{"none", []string{"-F", "this phrase is never planted in any log"}},
}

// runBench measures query latency: the time from sending a grep to every VM
// until all their results have been received, which is what the client does
// before it prints anything. Printing is left out on purpose, since rendering
// a frequent pattern's matches to a terminal takes longer than the query.
func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	trials := fs.Int("trials", 5, "measured queries per pattern and cluster size")
	warmup := fs.Int("warmup", 1, "unmeasured queries run first, so every log is already in the page cache")
	vmsFlag := fs.String("vms", "", "comma-separated cluster sizes, each using the first K hosts in host.txt (default: all hosts)")
	csvPath := fs.String("csv", "", "also write every measurement to this CSV file, for plotting")
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

	var rows [][]string
	fmt.Printf("%d measured trials per row, %d warm-up\n\n", *trials, *warmup)
	fmt.Printf("%-4s %-11s %10s %10s %10s %10s %10s\n", "VMs", "pattern", "matches", "mean", "stddev", "min", "max")

	for _, k := range sizes {
		for _, p := range benchPatterns {
			for range *warmup {
				if _, _, err := measureQuery(servers[:k], p.args); err != nil {
					fatalf("warm-up for %s on %d VMs: %v", p.label, k, err)
				}
			}

			latencies := make([]float64, *trials) // milliseconds
			matches := 0
			for i := range latencies {
				d, m, err := measureQuery(servers[:k], p.args)
				if err != nil {
					fatalf("%s on %d VMs: %v", p.label, k, err)
				}
				latencies[i] = float64(d.Microseconds()) / 1000
				matches = m
				rows = append(rows, []string{
					strconv.Itoa(k), p.label, strconv.Itoa(i + 1),
					strconv.FormatFloat(latencies[i], 'f', 3, 64), strconv.Itoa(m),
				})
			}

			mean, sd, lo, hi := summarize(latencies)
			fmt.Printf("%-4d %-11s %10d %8.1fms %8.1fms %8.1fms %8.1fms\n", k, p.label, matches, mean, sd, lo, hi)
		}
	}

	if *csvPath != "" {
		if err := writeCSV(*csvPath, rows); err != nil {
			fatalf("writing %s: %v", *csvPath, err)
		}
		fmt.Printf("\nwrote %d measurements to %s\n", len(rows), *csvPath)
	}
}

// measureQuery times one distributed query. A VM that fails makes the whole
// measurement invalid: the client would wait out its dial timeout, and the
// number would measure that timeout instead of grep.
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

// summarize returns the mean, sample standard deviation, min and max.
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

func writeCSV(path string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"vms", "pattern", "trial", "latency_ms", "matches"})
	w.WriteAll(rows)
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
