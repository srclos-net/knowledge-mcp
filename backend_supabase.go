package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type SupabaseBackend struct {
	cfg        SupabaseConfig
	pool       *pgxpool.Pool
	httpClient *http.Client
}

func NewSupabaseBackend(cfg SupabaseConfig) (*SupabaseBackend, error) {
	if cfg.Table == "" {
		cfg.Table = "learnings"
	}
	if cfg.EmbeddingDim == 0 {
		cfg.EmbeddingDim = 768
	}

	pool, err := pgxpool.New(context.Background(), cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("supabase: connect: %w", err)
	}

	b := &SupabaseBackend{
		cfg:        cfg,
		pool:       pool,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}

	if err := b.ensureSchema(); err != nil {
		pool.Close()
		return nil, fmt.Errorf("supabase: ensure schema: %w", err)
	}

	log.Printf("supabase backend: table=%s embedding_model=%q", cfg.Table, cfg.EmbeddingModel)
	return b, nil
}

func (b *SupabaseBackend) ensureSchema() error {
	ctx := context.Background()
	_, err := b.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector")
	if err != nil {
		return fmt.Errorf("create vector extension: %w", err)
	}

	createTable := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    id          TEXT PRIMARY KEY,
    category    TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL DEFAULT '',
    tags        TEXT NOT NULL DEFAULT '',
    confidence  DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    use_count   INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    embedding   vector(%d)
)`, b.cfg.Table, b.cfg.EmbeddingDim)

	if _, err := b.pool.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	createIndex := fmt.Sprintf(`
CREATE INDEX IF NOT EXISTS %s_embedding_idx
    ON %s USING hnsw (embedding vector_cosine_ops)`,
		b.cfg.Table, b.cfg.Table)

	if _, err := b.pool.Exec(ctx, createIndex); err != nil {
		return fmt.Errorf("create hnsw index: %w", err)
	}

	return nil
}

func (b *SupabaseBackend) Add(category, content, tags string, confidence float64) (*Learning, error) {
	now := time.Now()
	id := fmt.Sprintf("%d", now.UnixNano())

	var emb *pgvector.Vector
	if b.cfg.EmbeddingModel != "" {
		v, err := b.embed(content)
		if err != nil {
			log.Printf("supabase: embedding failed (storing without): %v", err)
		} else {
			pv := pgvector.NewVector(v)
			emb = &pv
		}
	}

	q := fmt.Sprintf(`
INSERT INTO %s (id, category, content, tags, confidence, use_count, created_at, updated_at, embedding)
VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8)`, b.cfg.Table)

	_, err := b.pool.Exec(context.Background(), q,
		id, category, content, tags, confidence, now, now, emb)
	if err != nil {
		return nil, fmt.Errorf("supabase add: %w", err)
	}

	return &Learning{
		ID: id, Category: category, Content: content,
		Tags: tags, Confidence: confidence,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (b *SupabaseBackend) Search(query, category string, limit int) ([]*Learning, error) {
	if limit <= 0 {
		limit = 10
	}

	ctx := context.Background()

	if b.cfg.EmbeddingModel != "" {
		v, err := b.embed(query)
		if err != nil {
			log.Printf("supabase: query embedding failed, falling back to text search: %v", err)
		} else {
			return b.vectorSearch(ctx, pgvector.NewVector(v), category, limit)
		}
	}

	return b.textSearch(ctx, query, category, limit)
}

func (b *SupabaseBackend) vectorSearch(ctx context.Context, emb pgvector.Vector, category string, limit int) ([]*Learning, error) {
	var (
		args  []any
		where string
	)
	args = append(args, emb, limit)

	if category != "" {
		args = append(args, category)
		where = fmt.Sprintf("WHERE category = $%d", len(args))
	}

	q := fmt.Sprintf(`
SELECT id, category, content, tags, confidence, use_count, created_at, updated_at
FROM %s
%s
ORDER BY embedding <=> $1
LIMIT $2`, b.cfg.Table, where)

	rows, err := b.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("supabase vector search: %w", err)
	}
	defer rows.Close()
	return scanPgxLearnings(rows)
}

func (b *SupabaseBackend) textSearch(ctx context.Context, query, category string, limit int) ([]*Learning, error) {
	var (
		conditions []string
		args       []any
	)

	args = append(args, "%"+query+"%")
	conditions = append(conditions, fmt.Sprintf("(content ILIKE $%d OR tags ILIKE $%d)", len(args), len(args)))

	if category != "" {
		args = append(args, category)
		conditions = append(conditions, fmt.Sprintf("category = $%d", len(args)))
	}

	args = append(args, limit)

	q := fmt.Sprintf(`
SELECT id, category, content, tags, confidence, use_count, created_at, updated_at
FROM %s
WHERE %s
ORDER BY updated_at DESC
LIMIT $%d`, b.cfg.Table, strings.Join(conditions, " AND "), len(args))

	rows, err := b.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("supabase text search: %w", err)
	}
	defer rows.Close()
	return scanPgxLearnings(rows)
}

func (b *SupabaseBackend) List(category string, limit int) ([]*Learning, error) {
	if limit <= 0 {
		limit = 50
	}

	var (
		where string
		args  []any
	)
	args = append(args, limit)

	if category != "" {
		args = append(args, category)
		where = fmt.Sprintf("WHERE category = $%d", len(args))
	}

	q := fmt.Sprintf(`
SELECT id, category, content, tags, confidence, use_count, created_at, updated_at
FROM %s
%s
ORDER BY updated_at DESC
LIMIT $1`, b.cfg.Table, where)

	rows, err := b.pool.Query(context.Background(), q, args...)
	if err != nil {
		return nil, fmt.Errorf("supabase list: %w", err)
	}
	defer rows.Close()
	return scanPgxLearnings(rows)
}

func (b *SupabaseBackend) Update(id, content, tags string, confidence float64) error {
	now := time.Now()

	var emb *pgvector.Vector
	if b.cfg.EmbeddingModel != "" {
		v, err := b.embed(content)
		if err != nil {
			log.Printf("supabase: update embedding failed: %v", err)
		} else {
			pv := pgvector.NewVector(v)
			emb = &pv
		}
	}

	q := fmt.Sprintf(`
UPDATE %s
SET content = $1, tags = $2, confidence = $3, updated_at = $4, embedding = $5
WHERE id = $6`, b.cfg.Table)

	tag, err := b.pool.Exec(context.Background(), q, content, tags, confidence, now, emb, id)
	if err != nil {
		return fmt.Errorf("supabase update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("supabase update: id not found: %s", id)
	}
	return nil
}

func (b *SupabaseBackend) Delete(id string) error {
	q := fmt.Sprintf("DELETE FROM %s WHERE id = $1", b.cfg.Table)
	_, err := b.pool.Exec(context.Background(), q, id)
	if err != nil {
		return fmt.Errorf("supabase delete: %w", err)
	}
	return nil
}

func (b *SupabaseBackend) IncrementUseCount(id string) {
	q := fmt.Sprintf("UPDATE %s SET use_count = use_count + 1 WHERE id = $1", b.cfg.Table)
	if _, err := b.pool.Exec(context.Background(), q, id); err != nil {
		log.Printf("supabase: increment use_count for %s: %v", id, err)
	}
}

func (b *SupabaseBackend) Stats() (map[string]int, error) {
	q := fmt.Sprintf("SELECT category, COUNT(*) FROM %s GROUP BY category", b.cfg.Table)
	rows, err := b.pool.Query(context.Background(), q)
	if err != nil {
		return nil, fmt.Errorf("supabase stats: %w", err)
	}
	defer rows.Close()

	stats := map[string]int{}
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err != nil {
			return nil, err
		}
		stats[cat] = count
	}
	return stats, rows.Err()
}

func (b *SupabaseBackend) Close() error {
	b.pool.Close()
	return nil
}

// embed calls Ollama to get a vector for text, then converts to []float32 for pgvector.
func (b *SupabaseBackend) embed(text string) ([]float32, error) {
	req := ollamaEmbedRequest{Model: b.cfg.EmbeddingModel, Prompt: text}
	body, _ := json.Marshal(req)
	resp, err := b.httpClient.Post(b.cfg.OllamaURL+"/api/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var embedResp ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, err
	}
	v := make([]float32, len(embedResp.Embedding))
	for i, f := range embedResp.Embedding {
		v[i] = float32(f)
	}
	return v, nil
}

// scanPgxLearnings reads pgx rows into a Learning slice. Columns:
// id, category, content, tags, confidence, use_count, created_at, updated_at
func scanPgxLearnings(rows pgx.Rows) ([]*Learning, error) {
	var out []*Learning
	for rows.Next() {
		l := &Learning{}
		if err := rows.Scan(&l.ID, &l.Category, &l.Content, &l.Tags,
			&l.Confidence, &l.UseCount, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
