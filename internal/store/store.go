// Package store persists crawl graphs so they can be compared over time.
//
// Persistence sits behind an interface so diff can work on the graph model
// rather than on whatever the backend happens to be. The default backend is
// gzipped JSON in a directory, standard library only. See ADR-0002.
package store

import (
	"context"
	"time"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// ID identifies one stored crawl.
type ID string

// Meta is the index entry for a stored crawl.
type Meta struct {
	ID         ID        `json:"id"`
	Target     string    `json:"target"`
	Label      string    `json:"label,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	Nodes      int       `json:"nodes"`
	Edges      int       `json:"edges"`
	Findings   int       `json:"findings"`
}

// Store persists and retrieves crawl graphs.
type Store interface {
	// Save writes a graph and returns its assigned ID.
	Save(ctx context.Context, g *sitegraph.Graph, label string) (ID, error)
	// List returns stored crawls, newest first. target "" means all targets.
	List(ctx context.Context, target string) ([]Meta, error)
	// Load retrieves a graph by ID.
	Load(ctx context.Context, id ID) (*sitegraph.Graph, error)
	// Resolve accepts a full ID, a unique prefix, "latest" or "latest:<target>".
	Resolve(ctx context.Context, ref string) (ID, error)
	// Close releases resources.
	Close() error
}
