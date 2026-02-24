package main

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteBackend struct {
	db *sql.DB
}

func NewSQLiteBackend(path string) (*SQLiteBackend, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	s := &SQLiteBackend{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	log.Printf("sqlite backend: %s", path)
	return s, nil
}

func (s *SQLiteBackend) migrate() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS learnings (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			category   TEXT NOT NULL DEFAULT 'general',
			content    TEXT NOT NULL,
			tags       TEXT NOT NULL DEFAULT '',
			confidence REAL NOT NULL DEFAULT 0.8,
			use_count  INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	// FTS5 is optional — falls back to per-word LIKE search if unavailable
	ftsStatements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS learnings_fts USING fts5(
			content, tags, category,
			content='learnings', content_rowid='id'
		)`,
		`CREATE TRIGGER IF NOT EXISTS learnings_ai AFTER INSERT ON learnings BEGIN
			INSERT INTO learnings_fts(rowid, content, tags, category)
			VALUES (new.id, new.content, new.tags, new.category);
		END`,
		`CREATE TRIGGER IF NOT EXISTS learnings_au AFTER UPDATE ON learnings BEGIN
			INSERT INTO learnings_fts(learnings_fts, rowid, content, tags, category)
			VALUES ('delete', old.id, old.content, old.tags, old.category);
			INSERT INTO learnings_fts(rowid, content, tags, category)
			VALUES (new.id, new.content, new.tags, new.category);
		END`,
		`CREATE TRIGGER IF NOT EXISTS learnings_ad AFTER DELETE ON learnings BEGIN
			INSERT INTO learnings_fts(learnings_fts, rowid, content, tags, category)
			VALUES ('delete', old.id, old.content, old.tags, old.category);
		END`,
	}
	for _, stmt := range ftsStatements {
		if _, err := s.db.Exec(stmt); err != nil {
			log.Printf("FTS5 unavailable (%v) — using LIKE fallback", err)
			break
		}
	}
	return nil
}

func (s *SQLiteBackend) Add(category, content, tags string, confidence float64) (*Learning, error) {
	now := time.Now()
	res, err := s.db.Exec(
		`INSERT INTO learnings (category, content, tags, confidence, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		category, content, tags, confidence, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Learning{
		ID: strconv.FormatInt(id, 10), Category: category, Content: content,
		Tags: tags, Confidence: confidence, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *SQLiteBackend) Search(query, category string, limit int) ([]*Learning, error) {
	if limit <= 0 {
		limit = 10
	}
	ftsQuery := strings.Join(strings.Fields(query), " OR ")
	baseSQL := `
		SELECT l.id, l.category, l.content, l.tags, l.confidence, l.use_count, l.created_at, l.updated_at
		FROM learnings l
		JOIN learnings_fts f ON l.id = f.rowid
		WHERE learnings_fts MATCH ?`
	args := []interface{}{ftsQuery}
	if category != "" {
		baseSQL += " AND l.category = ?"
		args = append(args, category)
	}
	baseSQL += " ORDER BY rank, l.confidence DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(baseSQL, args...)
	if err != nil {
		// Fallback: match any word via LIKE
		words := strings.Fields(query)
		var clauses []string
		var fargs []interface{}
		for _, w := range words {
			like := "%" + w + "%"
			clauses = append(clauses, "(content LIKE ? OR tags LIKE ?)")
			fargs = append(fargs, like, like)
		}
		if len(clauses) == 0 {
			clauses = append(clauses, "1=1")
		}
		fallback := `SELECT id, category, content, tags, confidence, use_count, created_at, updated_at
			FROM learnings WHERE (` + strings.Join(clauses, " OR ") + `)`
		if category != "" {
			fallback += " AND category = ?"
			fargs = append(fargs, category)
		}
		fallback += " ORDER BY confidence DESC, use_count DESC LIMIT ?"
		fargs = append(fargs, limit)
		rows, err = s.db.Query(fallback, fargs...)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()
	return scanLearnings(rows)
}

func (s *SQLiteBackend) List(category string, limit int) ([]*Learning, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, category, content, tags, confidence, use_count, created_at, updated_at FROM learnings`
	var args []interface{}
	if category != "" {
		q += " WHERE category = ?"
		args = append(args, category)
	}
	q += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLearnings(rows)
}

func (s *SQLiteBackend) Update(id, content, tags string, confidence float64) error {
	_, err := s.db.Exec(
		`UPDATE learnings SET content=?, tags=?, confidence=?, updated_at=? WHERE id=?`,
		content, tags, confidence, time.Now(), id,
	)
	return err
}

func (s *SQLiteBackend) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM learnings WHERE id=?`, id)
	return err
}

func (s *SQLiteBackend) IncrementUseCount(id string) {
	s.db.Exec(`UPDATE learnings SET use_count = use_count + 1 WHERE id=?`, id)
}

func (s *SQLiteBackend) Stats() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT category, COUNT(*) FROM learnings GROUP BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := map[string]int{}
	for rows.Next() {
		var cat string
		var count int
		rows.Scan(&cat, &count)
		stats[cat] = count
	}
	return stats, nil
}

func (s *SQLiteBackend) Close() error {
	return s.db.Close()
}

func scanLearnings(rows *sql.Rows) ([]*Learning, error) {
	var results []*Learning
	for rows.Next() {
		l := &Learning{}
		var idInt int64
		err := rows.Scan(&idInt, &l.Category, &l.Content, &l.Tags,
			&l.Confidence, &l.UseCount, &l.CreatedAt, &l.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		l.ID = strconv.FormatInt(idInt, 10)
		results = append(results, l)
	}
	return results, nil
}
