// Package frontier holds the crawl queue.
//
// One goroutine runs the frontier and owns the queue, the visited set and the
// in-flight counter, so none of them need locking. Everything else reaches it
// over channels.
package frontier

import (
	"container/heap"
	"context"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Task is one unit of crawl work handed to a worker.
type Task struct {
	URL   string // canonical
	Depth int
	Kind  sitegraph.NodeKind
}

// Candidate is a URL the builder has admitted to the graph and wants crawled.
type Candidate struct {
	URL   string
	Depth int
	Kind  sitegraph.NodeKind
}

// Config bounds the frontier.
type Config struct {
	// Workers sizes the ready channel (4x, so workers rarely idle).
	Workers int
	// MaxPages caps total dispatched tasks. 0 means unlimited.
	MaxPages int
	// MaxQueue caps the pending queue. Overflow is dropped and counted, never
	// dropped silently.
	MaxQueue int
}

// Stats reports what the frontier did.
type Stats struct {
	Dispatched    int
	Admitted      int
	DroppedQueue  int
	DroppedBudget int
	Duplicates    int
	PeakQueue     int
}

// Frontier is the crawl scheduler.
type Frontier struct {
	cfg Config

	ready chan Task
	cand  chan []Candidate
	ticks chan int

	queue   *taskHeap
	visited map[string]bool

	inflight int
	seq      int
	stats    Stats
}

// New builds a frontier.
func New(cfg Config) *Frontier {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 100_000
	}
	q := &taskHeap{}
	heap.Init(q)
	return &Frontier{
		cfg:     cfg,
		ready:   make(chan Task, cfg.Workers*4),
		cand:    make(chan []Candidate, cfg.Workers*2),
		ticks:   make(chan int, cfg.Workers*2),
		queue:   q,
		visited: make(map[string]bool),
	}
}

// Ready is the channel workers pull tasks from. Closed when the crawl is done.
func (f *Frontier) Ready() <-chan Task { return f.ready }

// Candidates is the channel the builder pushes newly admitted URLs to.
func (f *Frontier) Candidates() chan<- []Candidate { return f.cand }

// Complete is signalled by the builder once a task is fully processed.
func (f *Frontier) Complete() chan<- int { return f.ticks }

// Seed admits the starting URLs before the loop runs.
func (f *Frontier) Seed(cands []Candidate) {
	f.admit(cands)
}

func (f *Frontier) admit(cands []Candidate) {
	for _, c := range cands {
		if c.URL == "" {
			continue
		}
		if f.visited[c.URL] {
			f.stats.Duplicates++
			continue
		}
		if f.cfg.MaxPages > 0 && f.stats.Admitted >= f.cfg.MaxPages {
			f.stats.DroppedBudget++
			continue
		}
		if f.queue.Len() >= f.cfg.MaxQueue {
			f.stats.DroppedQueue++
			continue
		}
		f.visited[c.URL] = true
		f.seq++
		heap.Push(f.queue, &item{
			task: Task{URL: c.URL, Depth: c.Depth, Kind: c.Kind},
			seq:  f.seq,
		})
		f.stats.Admitted++
		if f.queue.Len() > f.stats.PeakQueue {
			f.stats.PeakQueue = f.queue.Len()
		}
	}
}

// Run drives the frontier until the crawl completes or ctx is cancelled,
// closing the ready channel on the way out.
//
// The select below offers the send and the receive at the same time. The
// pipeline is a cycle (frontier -> workers -> builder -> frontier) and every
// leg is a bounded channel, so a frontier parked on a send while the builder
// is also parked on a send deadlocks. Offering both arms keeps it draining.
//
// A WaitGroup can't end this: the crawl generates its own work, so there is no
// total to wait on. Done means empty queue and nothing in flight.
func (f *Frontier) Run(ctx context.Context) Stats {
	defer close(f.ready)

	for {
		// nil channels block forever in a select, so leaving out nil disables
		// the send arm when there is nothing queued.
		var out chan Task
		var next Task
		if f.queue.Len() > 0 {
			out = f.ready
			next = f.queue.peek()
		}

		if f.queue.Len() == 0 && f.inflight == 0 {
			return f.stats
		}

		select {
		case <-ctx.Done():
			return f.stats

		case out <- next:
			heap.Pop(f.queue)
			f.inflight++
			f.stats.Dispatched++

		case cs := <-f.cand:
			f.admit(cs)

		case n := <-f.ticks:
			f.inflight -= n
			if f.inflight < 0 {
				f.inflight = 0
			}
		}
	}
}

// Visited reports whether a URL has already been admitted.
func (f *Frontier) Visited(u string) bool { return f.visited[u] }

// --- priority queue -------------------------------------------------------

type item struct {
	task Task
	seq  int
	idx  int
}

// kindPriority ranks what to crawl first. Pages before assets, so a
// budget-limited crawl spends it on documents that yield more links.
func kindPriority(k sitegraph.NodeKind) int {
	switch k {
	case sitegraph.KindPage:
		return 0
	case sitegraph.KindAPI:
		return 1
	case sitegraph.KindScript:
		return 2
	case sitegraph.KindForm, sitegraph.KindDocument:
		return 3
	default:
		return 4
	}
}

type taskHeap []*item

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	// Breadth first, then by kind, then FIFO within a tier.
	if h[i].task.Depth != h[j].task.Depth {
		return h[i].task.Depth < h[j].task.Depth
	}
	pi, pj := kindPriority(h[i].task.Kind), kindPriority(h[j].task.Kind)
	if pi != pj {
		return pi < pj
	}
	return h[i].seq < h[j].seq
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx, h[j].idx = i, j
}

func (h *taskHeap) Push(x any) {
	it := x.(*item)
	it.idx = len(*h)
	*h = append(*h, it)
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return it
}

func (h *taskHeap) peek() Task { return (*h)[0].task }
