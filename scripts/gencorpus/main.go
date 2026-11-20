// Command gencorpus writes a synthetic mail corpus (every supported format,
// messy cases, optional bulk messages) for manual and performance testing,
// plus its ground-truth manifest as JSON. It is a development tool, not part
// of the product. Never commit generated corpora.
//
//	go run ./scripts/gencorpus -out /tmp/corpus [-seed 1] [-sets gmail,pst] [-bulk 100000]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/testutil/corpus"
)

func main() {
	out := flag.String("out", "", "output directory (must not exist or be empty)")
	seed := flag.Uint64("seed", 1, "random seed")
	sets := flag.String("sets", "", "comma-separated sets (default: all; "+strings.Join(append(corpus.AllSets, corpus.Bulk), ", ")+")")
	bulk := flag.Int("bulk", 0, "messages in the bulk set")
	thread := flag.Int("thread", 0, "length of the bulk reply chain")
	labels := flag.Int("labels", 0, "distinct labels in the bulk set (default 40)")
	bigMB := flag.Int("big-mb", 3, "size of big attachments/bodies in MiB")
	fileGB := flag.Float64("file-gb", 0, "grow the first bulk mbox to at least this many GiB")
	manifest := flag.String("manifest", "", "write the manifest JSON here (default <out>.manifest.json)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "gencorpus: -out is required")
		os.Exit(2)
	}
	if ents, err := os.ReadDir(*out); err == nil && len(ents) > 0 {
		fmt.Fprintln(os.Stderr, "gencorpus: -out is not empty")
		os.Exit(2)
	}
	o := corpus.Options{Seed: *seed, Bulk: *bulk, BulkThread: *thread, BulkLabels: *labels, BigBytes: *bigMB << 20,
		BulkFileBytes: int64(*fileGB * (1 << 30))}
	if *sets != "" {
		o.Sets = strings.Split(*sets, ",")
	}
	start := time.Now()
	m, err := corpus.Generate(*out, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gencorpus:", err)
		os.Exit(1)
	}
	mp := *manifest
	if mp == "" {
		mp = filepath.Clean(*out) + ".manifest.json"
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err == nil {
		err = os.WriteFile(mp, b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gencorpus:", err)
		os.Exit(1)
	}
	fmt.Printf("%d messages, %d sources, %.1f MiB in %v; manifest %s\n", len(m.Messages), len(m.Sources),
		float64(m.Bytes)/(1<<20), time.Since(start).Round(time.Millisecond), mp)
}
