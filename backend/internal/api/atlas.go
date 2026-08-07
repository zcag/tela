package api

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/zcag/tela/backend/internal/atlas/core"
	atlasllm "github.com/zcag/tela/backend/internal/atlas/llm"
	atlasstore "github.com/zcag/tela/backend/internal/atlas/store"
	"github.com/zcag/tela/backend/internal/auth"
)

// atlasEmbedDim is the embedding width — the atlas_chunks (and page_chunks)
// vector(1024) column. Used for the zero-vector last-resort below.
const atlasEmbedDim = 1024

// atlasManager owns the lifecycle of documentation-generation runs. It builds
// each run's engine inputs from tela env + the atlas_sources/atlas_runs rows,
// drives the lifted engine in a goroutine, streams live progress over the hub,
// and resumes dangling runs on boot. One active run per source.
//
// It reuses tela's instance backends end-to-end: chat goes to the lifted atlas
// client pointed at TELA_LLM_URL; embedding is delegated to s.rag.Embed (same
// Ollama, same model) via the client's EmbedFunc seam; publishing writes pages
// directly (atlasPublisher) and queues s.rag reindex.
type atlasManager struct {
	s     *Server
	store *atlasstore.Store
	hub   *atlasHub

	mu     sync.Mutex
	active map[int64]context.CancelFunc // sourceID -> cancel for its currently-executing run

	// maxRuns caps simultaneously-executing runs; dispatch nudges the durable
	// queue dispatcher (runDispatcher), which claims the next pending run from the
	// DB up to maxRuns whenever a slot frees. The queue lives in the DB
	// (status='pending'), so a restart/redeploy never strands a waiting run — on
	// boot the dispatcher simply picks the pending rows back up. Default cap 1 so
	// generation never clogs the shared LLM that interactive ask/research also use.
	maxRuns  int
	dispatch chan struct{}

	// paused is the admin AI kill-switch, shared with the other background
	// workers; the freshness scheduler skips a tick while it's true.
	paused func() bool
}

func newAtlasManager(s *Server) *atlasManager {
	return &atlasManager{
		s:        s,
		store:    atlasstore.New(s.DB),
		hub:      newAtlasHub(),
		active:   map[int64]context.CancelFunc{},
		maxRuns:  atlasMaxConcurrentRuns(),
		dispatch: make(chan struct{}, 1),
	}
}

// atlasMaxConcurrentRuns is the global cap on simultaneously-executing runs
// (TELA_ATLAS_MAX_CONCURRENT_RUNS, default 1). Kept at 1 by default on purpose:
// runs and interactive ask/research share one LLM endpoint, and overlapping
// drafts demonstrably overwhelmed it (502s). Raise only with a beefier endpoint.
func atlasMaxConcurrentRuns() int {
	if v := os.Getenv("TELA_ATLAS_MAX_CONCURRENT_RUNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// atlasEnabled reports whether a run can start: both the embedder and the chat
// LLM must be configured, or generation produces ungrounded garbage.
func (m *atlasManager) atlasEnabled() bool {
	return m.s.rag.Enabled() && os.Getenv("TELA_LLM_URL") != ""
}

// atlasModelCfg builds the engine's ModelCfg from tela's instance env. Chat hits
// TELA_LLM_URL (already /v1) with the same transport atlas calibrated against;
// EmbedModel is only a stats label — embeddings actually flow through s.rag via
// the client's EmbedFunc (see embedBatch), so they use tela's exact embedder.
func atlasModelCfg(embedModel string) core.ModelCfg {
	return core.ModelCfg{
		BaseURL:    os.Getenv("TELA_LLM_URL"),
		APIKey:     os.Getenv("TELA_LLM_TOKEN"),
		ChatModel:  atlasGetenv("TELA_LLM_MODEL", "qwen2.5:7b"),
		EmbedModel: embedModel,
		MaxTokens:  atlasMaxTokens(),
	}
}

// atlasMaxTokens caps each chat completion's output. Without it, an mlx_lm.server
// endpoint defaults to 512 and truncates long pages/outlines mid-JSON. Reads
// TELA_LLM_MAX_TOKENS (shared with tela's own LLM); a generous default otherwise.
func atlasMaxTokens() int {
	if v := os.Getenv("TELA_LLM_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8192
}

// newLLMClient constructs the lifted client and wires embedding to tela's
// rag.Embedder, so generation embeds with the SAME instance embedder + model
// tela's own RAG uses — identical vectors, shared metering, one endpoint.
func (m *atlasManager) newLLMClient() *atlasllm.Client {
	c := atlasllm.New(atlasModelCfg(m.s.rag.EmbedModel()))
	c.EmbedFunc = m.embedBatch
	return c
}

// embedBatch is the EmbedFunc seam: it embeds a batch through tela's
// rag.Embedder. Same contract as the client's built-in Embed — one vector per
// input, how many fell back to a zero vector, and how many UPSTREAM REQUESTS it
// took (the run's embed_calls, so the stats report real load rather than the
// caller's batch count).
//
// The whole batch goes in one rag.EmbedMany call. It used to loop rag.Embed
// per input, which quietly turned atlas's 16-chunk batches into 16 HTTP requests
// each — a 3.5k-file repo then hit the shared embed GPU ~20k times in half an
// hour and starved the chat model sharing that card.
//
// An embed error degrades to zero vectors (those chunks just won't be
// dense-retrievable) rather than failing the whole run — mirroring atlas's
// last-resort. ctx cancellation propagates as a hard error.
func (m *atlasManager) embedBatch(ctx context.Context, inputs []string) ([][]float32, int, int, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, 0, err
	}
	vecs, st, err := m.s.rag.EmbedMany(ctx, inputs)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, st.Requests, st.Tokens, ctxErr
		}
		out := make([][]float32, len(inputs))
		for i := range out {
			out[i] = make([]float32, atlasEmbedDim)
		}
		return out, len(inputs), st.Requests, st.Tokens, nil
	}
	return vecs, 0, st.Requests, st.Tokens, nil
}

// atlasOwnerManageErr gates MANAGEMENT of an owner-scoped atlas resource (a
// credential or a project): the caller must be the owner user, an admin of the
// owner org, or an instance admin. Management is admin-level on purpose — a run
// fetches an external source, spends LLM budget, and rewrites a generated
// subtree; a credential carries an access token. Returns nil when allowed.
func (s *Server) atlasOwnerManageErr(ctx context.Context, u *auth.User, ownerKind string, ownerID int64) *apiErr {
	if u.IsInstanceAdmin {
		return nil
	}
	switch ownerKind {
	case accountUser:
		if ownerID == u.ID {
			return nil
		}
	case accountOrg:
		if r, err := orgRole(ctx, s.DB, u.ID, ownerID); err == nil && r == orgRoleAdmin {
			return nil
		}
	default:
		return &apiErr{http.StatusBadRequest, "invalid_owner", "owner_kind must be 'user' or 'org'"}
	}
	return &apiErr{http.StatusForbidden, "forbidden", "atlas management required"}
}

// atlasOwnerViewErr gates VIEWING of an owner-scoped atlas resource: a personal
// owner reaches their own; an org owner is visible to every member of the org
// (admins manage, members read). Instance admins always pass.
func (s *Server) atlasOwnerViewErr(ctx context.Context, u *auth.User, ownerKind string, ownerID int64) *apiErr {
	if u.IsInstanceAdmin {
		return nil
	}
	switch ownerKind {
	case accountUser:
		if ownerID == u.ID {
			return nil
		}
	case accountOrg:
		if _, err := orgRole(ctx, s.DB, u.ID, ownerID); err == nil {
			return nil
		}
	default:
		return &apiErr{http.StatusBadRequest, "invalid_owner", "owner_kind must be 'user' or 'org'"}
	}
	return &apiErr{http.StatusForbidden, "forbidden", "not allowed to view this owner's atlas"}
}

// atlasProjectOwner resolves a project's owner scope (kind + id). sql.ErrNoRows
// when the project doesn't exist.
func (s *Server) atlasProjectOwner(ctx context.Context, projectID int64) (kind string, id int64, err error) {
	err = s.DB.QueryRowContext(ctx,
		`SELECT owner_kind, owner_id FROM atlas_projects WHERE id = $1`, projectID).Scan(&kind, &id)
	return kind, id, err
}

// atlasProjectManageErr loads a project's owner and applies the management gate.
func (s *Server) atlasProjectManageErr(ctx context.Context, u *auth.User, projectID int64) *apiErr {
	kind, id, err := s.atlasProjectOwner(ctx, projectID)
	if err == sql.ErrNoRows {
		return &apiErr{http.StatusNotFound, "not_found", "project not found"}
	} else if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup project failed"}
	}
	return s.atlasOwnerManageErr(ctx, u, kind, id)
}

// atlasProjectViewErr loads a project's owner and applies the view gate.
func (s *Server) atlasProjectViewErr(ctx context.Context, u *auth.User, projectID int64) *apiErr {
	kind, id, err := s.atlasProjectOwner(ctx, projectID)
	if err == sql.ErrNoRows {
		return &apiErr{http.StatusNotFound, "not_found", "project not found"}
	} else if err != nil {
		return &apiErr{http.StatusInternalServerError, "internal", "lookup project failed"}
	}
	return s.atlasOwnerViewErr(ctx, u, kind, id)
}

func atlasGetenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
