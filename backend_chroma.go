package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ChromaBackend struct {
	cfg          ChromaConfig
	httpClient   *http.Client
	collectionID string // UUID returned by Chroma after create/get
}

// ── Chroma v2 API types ───────────────────────────────────────────────────────

type chromaCollection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type chromaAddRequest struct {
	IDs        []string         `json:"ids"`
	Documents  []string         `json:"documents"`
	Metadatas  []map[string]any `json:"metadatas"`
	Embeddings [][]float64      `json:"embeddings,omitempty"`
}

type chromaQueryRequest struct {
	QueryTexts      []string        `json:"query_texts,omitempty"`
	QueryEmbeddings [][]float64     `json:"query_embeddings,omitempty"`
	NResults        int             `json:"n_results"`
	Where           map[string]any  `json:"where,omitempty"`
	Include         []string        `json:"include,omitempty"`
}

type chromaQueryResponse struct {
	IDs       [][]string         `json:"ids"`
	Documents [][]string         `json:"documents"`
	Metadatas [][]map[string]any `json:"metadatas"`
	Distances [][]float64        `json:"distances"`
}

type chromaGetRequest struct {
	IDs     []string       `json:"ids,omitempty"`
	Where   map[string]any `json:"where,omitempty"`
	Limit   int            `json:"limit,omitempty"`
	Include []string       `json:"include,omitempty"`
}

type chromaGetResponse struct {
	IDs       []string         `json:"ids"`
	Documents []string         `json:"documents"`
	Metadatas []map[string]any `json:"metadatas"`
}

type chromaUpdateRequest struct {
	IDs       []string         `json:"ids"`
	Documents []string         `json:"documents"`
	Metadatas []map[string]any `json:"metadatas"`
}

type chromaDeleteRequest struct {
	IDs []string `json:"ids"`
}

// ── Ollama embedding types ────────────────────────────────────────────────────

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float64 `json:"embedding"`
}

// ── Constructor ───────────────────────────────────────────────────────────────

func NewChromaBackend(cfg ChromaConfig) (*ChromaBackend, error) {
	b := &ChromaBackend{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	if err := b.ensureCollection(); err != nil {
		return nil, fmt.Errorf("chroma: ensure collection: %w", err)
	}
	log.Printf("chroma backend: %s (tenant=%s db=%s collection=%s id=%s)",
		cfg.URL, cfg.Tenant, cfg.Database, cfg.Collection, b.collectionID)
	return b, nil
}

// ── Path helpers ──────────────────────────────────────────────────────────────

// basePath returns /api/v2/tenants/{tenant}/databases/{database}
func (b *ChromaBackend) basePath() string {
	return fmt.Sprintf("/api/v2/tenants/%s/databases/%s", b.cfg.Tenant, b.cfg.Database)
}

// colPath returns the base path + /collections/{collection_id} + suffix
func (b *ChromaBackend) colPath(suffix string) string {
	return fmt.Sprintf("%s/collections/%s%s", b.basePath(), b.collectionID, suffix)
}

// ── Collection management ─────────────────────────────────────────────────────

func (b *ChromaBackend) ensureCollection() error {
	// List collections and find by name
	data, err := b.get(b.basePath() + "/collections")
	if err == nil {
		var cols []chromaCollection
		if json.Unmarshal(data, &cols) == nil {
			for _, c := range cols {
				if c.Name == b.cfg.Collection {
					b.collectionID = c.ID
					return nil
				}
			}
		}
	}

	// Create it
	body, _ := json.Marshal(map[string]any{
		"name":          b.cfg.Collection,
		"get_or_create": true,
	})
	resp, err := b.post(b.basePath()+"/collections", body)
	if err != nil {
		return err
	}
	var col chromaCollection
	if err := json.Unmarshal(resp, &col); err != nil {
		return fmt.Errorf("parse create collection response: %w", err)
	}
	b.collectionID = col.ID
	return nil
}

// ── Backend interface ─────────────────────────────────────────────────────────

func (b *ChromaBackend) Add(category, content, tags string, confidence float64) (*Learning, error) {
	now := time.Now()
	id := fmt.Sprintf("%d", now.UnixNano())

	req := chromaAddRequest{
		IDs:       []string{id},
		Documents: []string{content},
		Metadatas: []map[string]any{{
			"category":   category,
			"tags":       tags,
			"confidence": confidence,
			"use_count":  0,
			"created_at": now.Format(time.RFC3339),
			"updated_at": now.Format(time.RFC3339),
		}},
	}

	if b.cfg.EmbeddingModel != "" {
		emb, err := b.embed(content)
		if err != nil {
			log.Printf("embedding failed (storing without): %v", err)
		} else {
			req.Embeddings = [][]float64{emb}
		}
	}

	body, _ := json.Marshal(req)
	if _, err := b.post(b.colPath("/add"), body); err != nil {
		return nil, err
	}

	return &Learning{
		ID: id, Category: category, Content: content,
		Tags: tags, Confidence: confidence,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (b *ChromaBackend) Search(query, category string, limit int) ([]*Learning, error) {
	if limit <= 0 {
		limit = 10
	}

	req := chromaQueryRequest{
		NResults: limit,
		Include:  []string{"documents", "metadatas", "distances"},
	}

	if b.cfg.EmbeddingModel != "" {
		emb, err := b.embed(query)
		if err != nil {
			log.Printf("query embedding failed, falling back to text: %v", err)
			req.QueryTexts = []string{query}
		} else {
			req.QueryEmbeddings = [][]float64{emb}
		}
	} else {
		req.QueryTexts = []string{query}
	}

	if category != "" {
		req.Where = map[string]any{"category": map[string]any{"$eq": category}}
	}

	body, _ := json.Marshal(req)
	data, err := b.post(b.colPath("/query"), body)
	if err != nil {
		return nil, err
	}

	var resp chromaQueryResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse query response: %w", err)
	}
	if len(resp.IDs) == 0 || len(resp.IDs[0]) == 0 {
		return nil, nil
	}

	return chromaResultsToLearnings(resp.IDs[0], resp.Documents[0], resp.Metadatas[0]), nil
}

func (b *ChromaBackend) List(category string, limit int) ([]*Learning, error) {
	if limit <= 0 {
		limit = 50
	}

	req := chromaGetRequest{
		Limit:   limit,
		Include: []string{"documents", "metadatas"},
	}
	if category != "" {
		req.Where = map[string]any{"category": map[string]any{"$eq": category}}
	}

	body, _ := json.Marshal(req)
	data, err := b.post(b.colPath("/get"), body)
	if err != nil {
		return nil, err
	}

	var resp chromaGetResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}

	learnings := chromaGetToLearnings(resp)
	sortByUpdated(learnings)
	return learnings, nil
}

func (b *ChromaBackend) Update(id, content, tags string, confidence float64) error {
	now := time.Now()

	existing, _ := b.getByID(id)
	useCount := 0
	category := "general"
	createdAt := now.Format(time.RFC3339)
	if existing != nil {
		useCount = existing.UseCount
		category = existing.Category
		createdAt = existing.CreatedAt.Format(time.RFC3339)
	}

	req := chromaUpdateRequest{
		IDs:       []string{id},
		Documents: []string{content},
		Metadatas: []map[string]any{{
			"category":   category,
			"tags":       tags,
			"confidence": confidence,
			"use_count":  useCount,
			"created_at": createdAt,
			"updated_at": now.Format(time.RFC3339),
		}},
	}

	body, _ := json.Marshal(req)
	_, err := b.post(b.colPath("/update"), body)
	return err
}

func (b *ChromaBackend) Delete(id string) error {
	req := chromaDeleteRequest{IDs: []string{id}}
	body, _ := json.Marshal(req)
	_, err := b.post(b.colPath("/delete"), body)
	return err
}

func (b *ChromaBackend) IncrementUseCount(id string) {
	existing, err := b.getByID(id)
	if err != nil || existing == nil {
		return
	}
	req := chromaUpdateRequest{
		IDs:       []string{id},
		Documents: []string{existing.Content},
		Metadatas: []map[string]any{{
			"category":   existing.Category,
			"tags":       existing.Tags,
			"confidence": existing.Confidence,
			"use_count":  existing.UseCount + 1,
			"created_at": existing.CreatedAt.Format(time.RFC3339),
			"updated_at": existing.UpdatedAt.Format(time.RFC3339),
		}},
	}
	body, _ := json.Marshal(req)
	b.post(b.colPath("/update"), body)
}

func (b *ChromaBackend) Stats() (map[string]int, error) {
	req := chromaGetRequest{Include: []string{"metadatas"}}
	body, _ := json.Marshal(req)
	data, err := b.post(b.colPath("/get"), body)
	if err != nil {
		return nil, err
	}

	var resp chromaGetResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}

	stats := map[string]int{}
	for _, meta := range resp.Metadatas {
		if cat, ok := meta["category"].(string); ok {
			stats[cat]++
		}
	}
	return stats, nil
}

func (b *ChromaBackend) Close() error { return nil }

// ── Internal helpers ──────────────────────────────────────────────────────────

func (b *ChromaBackend) getByID(id string) (*Learning, error) {
	req := chromaGetRequest{
		IDs:     []string{id},
		Include: []string{"documents", "metadatas"},
	}
	body, _ := json.Marshal(req)
	data, err := b.post(b.colPath("/get"), body)
	if err != nil {
		return nil, err
	}
	var resp chromaGetResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	results := chromaGetToLearnings(resp)
	if len(results) == 0 {
		return nil, fmt.Errorf("not found: %s", id)
	}
	return results[0], nil
}

func (b *ChromaBackend) embed(text string) ([]float64, error) {
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
	return embedResp.Embedding, nil
}

func (b *ChromaBackend) get(path string) ([]byte, error) {
	resp, err := b.httpClient.Get(b.cfg.URL + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chroma GET %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (b *ChromaBackend) post(path string, body []byte) ([]byte, error) {
	resp, err := b.httpClient.Post(b.cfg.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chroma POST %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// ── Conversion helpers ────────────────────────────────────────────────────────

func chromaResultsToLearnings(ids, docs []string, metas []map[string]any) []*Learning {
	var out []*Learning
	for i := range ids {
		out = append(out, metaToLearning(ids[i], docs[i], metas[i]))
	}
	return out
}

func chromaGetToLearnings(resp chromaGetResponse) []*Learning {
	var out []*Learning
	for i := range resp.IDs {
		doc := ""
		if i < len(resp.Documents) {
			doc = resp.Documents[i]
		}
		meta := map[string]any{}
		if i < len(resp.Metadatas) {
			meta = resp.Metadatas[i]
		}
		out = append(out, metaToLearning(resp.IDs[i], doc, meta))
	}
	return out
}

func metaToLearning(id, doc string, meta map[string]any) *Learning {
	l := &Learning{ID: id, Content: doc}
	if v, ok := meta["category"].(string); ok {
		l.Category = v
	}
	if v, ok := meta["tags"].(string); ok {
		l.Tags = v
	}
	if v, ok := meta["confidence"].(float64); ok {
		l.Confidence = v
	}
	if v, ok := meta["use_count"]; ok {
		switch n := v.(type) {
		case float64:
			l.UseCount = int(n)
		case int:
			l.UseCount = n
		}
	}
	if v, ok := meta["created_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			l.CreatedAt = t
		}
	}
	if v, ok := meta["updated_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			l.UpdatedAt = t
		}
	}
	return l
}

func sortByUpdated(ls []*Learning) {
	sort.Slice(ls, func(i, j int) bool {
		return ls[i].UpdatedAt.After(ls[j].UpdatedAt)
	})
}
