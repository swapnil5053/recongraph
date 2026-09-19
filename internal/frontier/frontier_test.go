package frontier

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Seeds, no feedback: dispatch everything and stop.
func TestTerminatesWithNoFeedback(t *testing.T) {
	f := New(Config{Workers: 2})
	f.Seed([]Candidate{
		{URL: "https://e.com/a", Kind: sitegraph.KindPage},
		{URL: "https://e.com/b", Kind: sitegraph.KindPage},
	})

	var got []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range f.Ready() {
				mu.Lock()
				got = append(got, task.URL)
				mu.Unlock()
				f.Complete() <- 1
			}
		}()
	}

	stats := runWithTimeout(t, f)
	wg.Wait()

	if len(got) != 2 {
		t.Fatalf("dispatched %d tasks, want 2: %v", len(got), got)
	}
	if stats.Dispatched != 2 {
		t.Errorf("stats.Dispatched = %d, want 2", stats.Dispatched)
	}
}

// Workers feeding candidates back in. Must still terminate exactly when the
// generated tree is exhausted.
func TestTerminatesWithCyclicFeedback(t *testing.T) {
	f := New(Config{Workers: 4})
	f.Seed([]Candidate{{URL: "https://e.com/0", Depth: 0, Kind: sitegraph.KindPage}})

	const maxDepth = 3
	const fanout = 3

	var mu sync.Mutex
	seen := map[string]bool{}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range f.Ready() {
				mu.Lock()
				if seen[task.URL] {
					t.Errorf("URL dispatched twice: %s", task.URL)
				}
				seen[task.URL] = true
				mu.Unlock()

				var next []Candidate
				if task.Depth < maxDepth {
					for c := 0; c < fanout; c++ {
						next = append(next, Candidate{
							URL:   fmt.Sprintf("%s/%d", task.URL, c),
							Depth: task.Depth + 1,
							Kind:  sitegraph.KindPage,
						})
					}
				}
				if len(next) > 0 {
					f.Candidates() <- next
				}
				f.Complete() <- 1
			}
		}()
	}

	stats := runWithTimeout(t, f)
	wg.Wait()

	// 1 + 3 + 9 + 27 = 40 nodes in the generated tree.
	want := 1 + fanout + fanout*fanout + fanout*fanout*fanout
	if stats.Dispatched != want {
		t.Errorf("Dispatched = %d, want %d", stats.Dispatched, want)
	}
	if len(seen) != want {
		t.Errorf("unique URLs = %d, want %d", len(seen), want)
	}
}

// Same URL from many pages, admitted once.
func TestDeduplicates(t *testing.T) {
	f := New(Config{Workers: 1})
	f.Seed([]Candidate{{URL: "https://e.com/x"}, {URL: "https://e.com/x"}, {URL: "https://e.com/y"}})

	var n int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range f.Ready() {
			n++
			f.Complete() <- 1
		}
	}()
	stats := runWithTimeout(t, f)
	wg.Wait()

	if n != 2 {
		t.Errorf("dispatched %d, want 2", n)
	}
	if stats.Duplicates != 1 {
		t.Errorf("Duplicates = %d, want 1", stats.Duplicates)
	}
}

// Budget stops admission and gets reported.
func TestMaxPagesBudget(t *testing.T) {
	f := New(Config{Workers: 2, MaxPages: 5})
	var seeds []Candidate
	for i := 0; i < 20; i++ {
		seeds = append(seeds, Candidate{URL: fmt.Sprintf("https://e.com/%d", i)})
	}
	f.Seed(seeds)

	var n int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range f.Ready() {
			n++
			f.Complete() <- 1
		}
	}()
	stats := runWithTimeout(t, f)
	wg.Wait()

	if n != 5 {
		t.Errorf("dispatched %d, want 5", n)
	}
	if stats.DroppedBudget != 15 {
		t.Errorf("DroppedBudget = %d, want 15", stats.DroppedBudget)
	}
}

// Cancellation stops the frontier and closes ready.
func TestCancellation(t *testing.T) {
	f := New(Config{Workers: 1})
	var seeds []Candidate
	for i := 0; i < 1000; i++ {
		seeds = append(seeds, Candidate{URL: fmt.Sprintf("https://e.com/%d", i)})
	}
	f.Seed(seeds)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()

	// Consume a couple then cancel.
	<-f.Ready()
	<-f.Ready()
	cancel()

	// Drain whatever is buffered so the frontier is not blocked on a send.
	go func() {
		for range f.Ready() {
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("frontier did not stop within 2s of cancellation")
	}
}

// Everything at depth 0 before anything at depth 1.
func TestBreadthFirstOrdering(t *testing.T) {
	f := New(Config{Workers: 1})
	f.Seed([]Candidate{
		{URL: "https://e.com/deep", Depth: 5, Kind: sitegraph.KindPage},
		{URL: "https://e.com/shallow", Depth: 0, Kind: sitegraph.KindPage},
		{URL: "https://e.com/mid", Depth: 2, Kind: sitegraph.KindPage},
	})

	var order []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for task := range f.Ready() {
			order = append(order, task.URL)
			f.Complete() <- 1
		}
	}()
	runWithTimeout(t, f)
	wg.Wait()

	want := []string{"https://e.com/shallow", "https://e.com/mid", "https://e.com/deep"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// Pages outrank assets at the same depth.
func TestKindPriorityWithinDepth(t *testing.T) {
	f := New(Config{Workers: 1})
	f.Seed([]Candidate{
		{URL: "https://e.com/img.png", Depth: 1, Kind: sitegraph.KindImage},
		{URL: "https://e.com/app.js", Depth: 1, Kind: sitegraph.KindScript},
		{URL: "https://e.com/page", Depth: 1, Kind: sitegraph.KindPage},
	})

	var order []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for task := range f.Ready() {
			order = append(order, task.URL)
			f.Complete() <- 1
		}
	}()
	runWithTimeout(t, f)
	wg.Wait()

	want := []string{"https://e.com/page", "https://e.com/app.js", "https://e.com/img.png"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func runWithTimeout(t *testing.T, f *Frontier) Stats {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	statsCh := make(chan Stats, 1)
	go func() { statsCh <- f.Run(ctx) }()
	select {
	case s := <-statsCh:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("frontier did not terminate (deadlock in the cyclic pipeline?)")
		return Stats{}
	}
}
