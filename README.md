# knowledge-mcp

A self-hosted MCP (Model Context Protocol) server that gives AI assistants persistent memory across conversations. The AI looks up relevant context at the start of each session and writes back learnings it discovers — preferences, mistakes to avoid, personal context, communication patterns — so every future conversation is better informed than the last.

Built in Go. Stores data in either **SQLite** (simple, self-contained) or **ChromaDB** (semantic search, existing infrastructure). Speaks the MCP streamable HTTP transport, compatible with open-webui and other MCP clients.

---

## How it works

At the start of a conversation the AI calls `lookup_context` with keywords describing what you're working on. The server searches stored learnings and returns relevant ones — things like your communication preferences, past mistakes the AI made, relevant personal context, or technical details about your stack. The AI uses this to calibrate its response before saying a word.

At the end of a session (or whenever something useful is discovered), the AI calls `store_learning` to persist it. Over time the store builds up a detailed, searchable picture of how to work with you effectively.

The AI writes directly — no human approval step. You can review, edit, or prune entries at any time using `list_learnings`, `update_learning`, and `delete_learning`.

---

## Tools exposed

| Tool | Description |
|------|-------------|
| `lookup_context` | **Call this first.** Searches stored learnings by keyword and returns relevant ones. Increments use count on returned results. |
| `store_learning` | Stores a new learning with category, content, tags, and confidence score. |
| `list_learnings` | Lists all stored learnings, optionally filtered by category. |
| `update_learning` | Updates an existing learning by ID. |
| `delete_learning` | Deletes a learning by ID. |
| `get_stats` | Returns a count of learnings per category. |

### Categories

| Category | Purpose |
|----------|---------|
| `preferences` | Communication style, formatting preferences, things to always/never do |
| `personal_context` | Relevant personal facts that inform better responses |
| `technical` | Stack details, tools in use, technical preferences |
| `personal_growth` | Ongoing work, patterns, things the person is working through |
| `mistakes` | Things that went wrong and how to avoid repeating them |
| `general` | Catch-all for anything that doesn't fit above |

---

## Backends

### SQLite (default)

Simple, self-contained. A single file on disk. Uses FTS5 full-text search when available (Alpine Linux includes it), falls back to per-word `LIKE` queries otherwise. Good for single-instance deployments.

The database is created automatically on first run — no setup needed beyond ensuring the directory exists.

### ChromaDB

Uses your existing ChromaDB HTTP API (v2). Stores documents with metadata. If you configure an Ollama embedding model, embeddings are generated via Ollama and passed to Chroma, giving you real semantic search rather than keyword matching. Without an embedding model configured, Chroma uses its own default embedder.

Requires ChromaDB ≥ 0.6 (API v2).

---

## Configuration

Configuration is a TOML file. Print an example with:

```bash
./self-improvement-mcp --print-config
```

### Full reference

```toml
[server]
addr = ":8080"          # Listen address

[backend]
type = "sqlite"         # "sqlite" or "chroma"

[sqlite]
path = "/data/learnings.db"   # Path to SQLite file; created automatically

[chroma]
url        = "http://chroma:8000"
tenant     = "default_tenant"    # Chroma v2 tenant (default: "default_tenant")
database   = "default_database"  # Chroma v2 database (default: "default_database")
collection = "self_improvement"  # Collection name; created automatically if missing

# Optional: use Ollama for semantic embeddings
# embedding_model = "nomic-embed-text"
# ollama_url      = "http://ollama:11434"
```

### Config file resolution order

The server looks for a config file in this order, stopping at the first one found:

1. `--config <path>` flag
2. `CONFIG_FILE` environment variable
3. `./config.toml` (current directory)
4. `/config/config.toml`
5. `/etc/self-improvement-mcp/config.toml`

If no config file is found, all defaults apply (SQLite backend, port 8080, `/data/learnings.db`).

---

## Running locally

### Build from source

Requires Go 1.22+ and a C compiler (for SQLite CGO bindings).

```bash
git clone <repo>
cd self-improvement-mcp
go build -o self-improvement-mcp .
./self-improvement-mcp --print-config > config.toml
# edit config.toml as needed
./self-improvement-mcp --config config.toml
```

### Run with SQLite

```bash
mkdir -p data
./self-improvement-mcp --config config.toml
```

The SQLite database is created automatically. No `touch` or pre-initialization needed.

### Run with ChromaDB

Update `config.toml`:

```toml
[backend]
type = "chroma"

[chroma]
url        = "http://192.168.1.20:8001"
collection = "self_improvement"
```

The collection is created automatically on first run if it doesn't exist.

---

## Running with Docker

### Build the image

```bash
docker build -t self-improvement-mcp:latest .
```

The Dockerfile uses a multi-stage Alpine build. The final image is ~15MB.

### Run with SQLite

```bash
docker run -d \
  --name self-improvement-mcp \
  -p 8080:8080 \
  -v $(pwd)/config.toml:/config/config.toml:ro \
  -v self-improvement-data:/data \
  self-improvement-mcp:latest
```

### Run with ChromaDB

```bash
docker run -d \
  --name self-improvement-mcp \
  -p 8080:8080 \
  -v $(pwd)/config.toml:/config/config.toml:ro \
  self-improvement-mcp:latest
```

### Connecting from another container

If your MCP client (e.g. open-webui) runs in a container and the server runs on the Docker host, use:

```
http://host.docker.internal:8080/mcp
```

On Linux, `host.docker.internal` requires explicit configuration. Add to your client container's compose service:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

---

## Kubernetes deployment

A complete manifest is provided in `k8s.yaml`. It includes a ConfigMap, PVC (for SQLite), Deployment, and Service, all in the `ai` namespace.

### Deploy

```bash
# Update the image reference in k8s.yaml first
kubectl apply -f k8s.yaml
```

### Switch backends via ConfigMap

To switch from SQLite to ChromaDB, edit the ConfigMap and update `type = "chroma"` — no rebuild or redeploy needed, just a rolling restart:

```bash
kubectl edit configmap self-improvement-mcp-config -n ai
kubectl rollout restart deployment/self-improvement-mcp -n ai
```

### Service URL (from within the cluster)

```
http://self-improvement-mcp.ai.svc.cluster.local:8080/mcp
```

---

## Connecting to open-webui

In open-webui, add a new MCP tool server:

- **URL:** `http://host.docker.internal:8080/mcp` (if running on Docker host)
- **Transport:** Streamable HTTP

The server uses the MCP `instructions` field in the `initialize` response to tell the AI to call `lookup_context` at the start of every conversation. How strictly this is followed depends on the model and open-webui's system prompt configuration.

---

## Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/mcp` | `POST` | MCP JSON-RPC endpoint (streamable HTTP) |
| `/mcp` | `GET` | SSE stream for server-initiated messages |
| `/health` | `GET` | Health check — returns `{"status":"ok","version":"1.0.0"}` |

---

## MCP protocol

The server implements [MCP 2024-11-05](https://modelcontextprotocol.io) over streamable HTTP. Supported methods:

- `initialize`
- `notifications/initialized`
- `ping`
- `tools/list`
- `tools/call`
- Batch requests (JSON array)

---

## Project structure

```
self-improvement-mcp/
├── main.go              # Entry point, config loading, server startup
├── config.go            # TOML config types and defaults
├── backend.go           # Backend interface + factory
├── backend_sqlite.go    # SQLite implementation (FTS5 + LIKE fallback)
├── backend_chroma.go    # ChromaDB v2 HTTP API implementation
├── server.go            # Streamable HTTP MCP server
├── tools.go             # Tool definitions and handlers
├── Dockerfile           # Multi-stage Alpine build
└── k8s.yaml             # Kubernetes manifests (ConfigMap, PVC, Deployment, Service)
```

---

## Adding a new backend

Implement the `Backend` interface in `backend.go`:

```go
type Backend interface {
    Add(category, content, tags string, confidence float64) (*Learning, error)
    Search(query, category string, limit int) ([]*Learning, error)
    List(category string, limit int) ([]*Learning, error)
    Update(id, content, tags string, confidence float64) error
    Delete(id string) error
    IncrementUseCount(id string)
    Stats() (map[string]int, error)
    Close() error
}
```

Then add a case to the `NewBackend` factory in `backend.go` and a new config section in `config.go`.

---

## Dependencies

| Dependency | Purpose |
|------------|---------|
| `github.com/mattn/go-sqlite3` | SQLite driver (CGO) |
| `github.com/BurntSushi/toml` | TOML config parsing |

No other runtime dependencies. ChromaDB and Ollama are external services, not Go dependencies.

---

## License

MIT
