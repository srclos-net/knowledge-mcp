package main

import "time"

// Learning is the core data type shared across backends.
type Learning struct {
	ID         string    `json:"id"`
	Category   string    `json:"category"`
	Content    string    `json:"content"`
	Tags       string    `json:"tags"`
	Confidence float64   `json:"confidence"`
	UseCount   int       `json:"use_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Backend is the storage interface. Both SQLite and ChromaDB implement this.
type Backend interface {
	// Add stores a new learning and returns it with its assigned ID.
	Add(category, content, tags string, confidence float64) (*Learning, error)

	// Search returns learnings relevant to the query, optionally filtered by category.
	Search(query, category string, limit int) ([]*Learning, error)

	// List returns all learnings, optionally filtered by category, newest first.
	List(category string, limit int) ([]*Learning, error)

	// Update replaces the content/tags/confidence of an existing learning.
	Update(id, content, tags string, confidence float64) error

	// Delete removes a learning by ID.
	Delete(id string) error

	// IncrementUseCount records that a learning was surfaced to the AI.
	IncrementUseCount(id string)

	// Stats returns a count of learnings per category.
	Stats() (map[string]int, error)

	// Close releases any resources held by the backend.
	Close() error
}

// NewBackend constructs the appropriate backend from config.
func NewBackend(cfg *Config) (Backend, error) {
	switch cfg.Backend.Type {
	case "sqlite", "":
		return NewSQLiteBackend(cfg.SQLite.Path)
	case "chroma":
		return NewChromaBackend(cfg.Chroma)
	default:
		return nil, nil
	}
}
