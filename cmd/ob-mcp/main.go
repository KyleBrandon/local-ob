package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/KyleBrandon/local-ob/internal/classify"
	"github.com/KyleBrandon/local-ob/internal/db"
	"github.com/KyleBrandon/local-ob/internal/embed"
)

func main() {
	httpAddr := flag.String("http", "", "if set, serve MCP over HTTP (e.g. :8080)")
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if /health is reachable")
	flag.Parse()
	_ = godotenv.Load()

	if *healthcheck {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	ctx := context.Background()

	// --- providers ---
	dim, _ := strconv.Atoi(os.Getenv("EMBED_DIMENSION"))
	embedProv, err := embed.New(embed.Config{
		Provider:  os.Getenv("EMBED_PROVIDER"),
		Model:     os.Getenv("EMBED_MODEL"),
		Dimension: dim,
		APIKey:    os.Getenv("EMBED_API_KEY"),
		BaseURL:   os.Getenv("EMBED_BASE_URL"),
	})
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	classifyProv, err := classify.New(classify.Config{
		Provider: os.Getenv("CLASSIFY_PROVIDER"),
		Model:    os.Getenv("CLASSIFY_MODEL"),
		APIKey:   os.Getenv("CLASSIFY_API_KEY"),
		BaseURL:  os.Getenv("CLASSIFY_BASE_URL"),
	})
	if err != nil {
		log.Fatalf("classify: %v", err)
	}

	// --- database ---
	database, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	// --- startup safety gates ---
	if err := assertProviderAlignment(ctx, database, embedProv,
		os.Getenv("FORCE_PROVIDER_CHANGE") == "true"); err != nil {
		log.Fatalf("startup: %v", err)
	}

	log.Printf("embed: %s/%s (dim=%d)", embedProv.Name(), embedProv.Model(), embedProv.Dimension())
	log.Printf("classify: %s/%s", classifyProv.Name(), classifyProv.Model())

	// --- MCP server ---
	s := server.NewMCPServer("open-brain-local", "0.1.0")
	registerTools(s, database, embedProv, classifyProv)

	if *httpAddr != "" {
		mcpHandler := server.NewStreamableHTTPServer(s)
		mux := http.NewServeMux()
		mux.Handle("/ob-mcp", mcpHandler)
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := database.Ping(pingCtx); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})

		certFile, keyFile := os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE")
		if certFile != "" && keyFile != "" {
			log.Printf("HTTPS MCP on %s (path /ob-mcp, health /health)", *httpAddr)
			if err := http.ListenAndServeTLS(*httpAddr, certFile, keyFile, mux); err != nil {
				log.Fatal(err)
			}
			return
		}
		log.Printf("HTTP MCP on %s (path /ob-mcp, health /health)", *httpAddr)
		if err := http.ListenAndServe(*httpAddr, mux); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := server.ServeStdio(s); err != nil {
		log.Fatal(err)
	}
}

func assertProviderAlignment(ctx context.Context, d *db.DB, p embed.Provider, force bool) error {
	schemaDim, err := d.SchemaDim(ctx)
	if err != nil {
		return fmt.Errorf("read schema dim: %w", err)
	}
	if p.Dimension() != schemaDim {
		return fmt.Errorf("dim mismatch: provider %s returns %d, schema expects %d",
			p.Name(), p.Dimension(), schemaDim)
	}
	lastP, lastM, err := d.LastRowProvider(ctx)
	if err != nil {
		return fmt.Errorf("read last row: %w", err)
	}
	if lastP == "" {
		return nil
	}
	mismatch := lastP != p.Name() || lastM != p.Model()
	if mismatch && !force {
		return fmt.Errorf(
			"provider/model mismatch: existing rows use %s/%s, configured %s/%s.\n"+
				"  Re-embed with 'ob-reembed', or set FORCE_PROVIDER_CHANGE=true to proceed\n"+
				"  (new rows will live in a different vector space than old ones).",
			lastP, lastM, p.Name(), p.Model())
	}
	if mismatch && force {
		log.Printf("WARNING: provider change forced. Old rows (%s/%s) will not cross-match new rows (%s/%s).",
			lastP, lastM, p.Name(), p.Model())
	}
	return nil
}

func registerTools(s *server.MCPServer, d *db.DB, e embed.Provider, c classify.Provider) {
	s.AddTool(
		mcp.NewTool("capture_thought",
			mcp.WithDescription("Save a thought to your personal knowledge brain."),
			mcp.WithString("content", mcp.Required(),
				mcp.Description("Self-contained statement, ideally one idea per entry.")),
			mcp.WithString("source",
				mcp.Description("Origin hint, e.g. 'claude-desktop'")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content, err := req.RequireString("content")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			source := req.GetString("source", "")

			var (
				emb    []float32
				md     classify.Metadata
				wg     sync.WaitGroup
				embErr error
			)
			wg.Add(2)
			go func() { defer wg.Done(); emb, embErr = e.Embed(ctx, content) }()
			go func() { defer wg.Done(); md, _ = c.Classify(ctx, content) }()
			wg.Wait()

			if embErr != nil {
				return mcp.NewToolResultError("embed failed: " + embErr.Error()), nil
			}
			id, err := d.Insert(ctx, content, md.ThoughtType, source,
				e.Name(), e.Model(), md.Topics, md.People, emb)
			if err != nil {
				return mcp.NewToolResultError("insert failed: " + err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Captured %s (%s)", id, md.ThoughtType)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("update_thought",
			mcp.WithDescription("Update an existing thought's content. Re-embeds and re-classifies automatically."),
			mcp.WithString("id", mcp.Required(),
				mcp.Description("UUID of the thought to update.")),
			mcp.WithString("content", mcp.Required(),
				mcp.Description("New content; replaces the existing thought.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			content, err := req.RequireString("content")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			var (
				emb    []float32
				md     classify.Metadata
				wg     sync.WaitGroup
				embErr error
			)
			wg.Add(2)
			go func() { defer wg.Done(); emb, embErr = e.Embed(ctx, content) }()
			go func() { defer wg.Done(); md, _ = c.Classify(ctx, content) }()
			wg.Wait()

			if embErr != nil {
				return mcp.NewToolResultError("embed failed: " + embErr.Error()), nil
			}
			found, err := d.Update(ctx, id, content, md.ThoughtType,
				e.Name(), e.Model(), md.Topics, md.People, emb)
			if err != nil {
				return mcp.NewToolResultError("update failed: " + err.Error()), nil
			}
			if !found {
				return mcp.NewToolResultError("no thought with id " + id), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Updated %s (%s)", id, md.ThoughtType)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("delete_thought",
			mcp.WithDescription("Permanently delete a thought by ID."),
			mcp.WithString("id", mcp.Required(),
				mcp.Description("UUID of the thought to delete.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			found, err := d.Delete(ctx, id)
			if err != nil {
				return mcp.NewToolResultError("delete failed: " + err.Error()), nil
			}
			if !found {
				return mcp.NewToolResultError("no thought with id " + id), nil
			}
			return mcp.NewToolResultText("Deleted " + id), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_thoughts",
			mcp.WithDescription("List recent thoughts with optional filters by type, topic, person, or recency window."),
			mcp.WithNumber("limit",
				mcp.Description("Max rows to return (default 10).")),
			mcp.WithString("type",
				mcp.Description("Filter by thought_type (e.g. idea, fact, task, question, decision, observation).")),
			mcp.WithString("topic",
				mcp.Description("Filter by a single topic tag; matches rows whose topics array contains it.")),
			mcp.WithString("person",
				mcp.Description("Filter by a single person; matches rows whose people array contains them.")),
			mcp.WithNumber("days",
				mcp.Description("Only include rows from the last N days (0 = no limit).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			limit := req.GetInt("limit", 10)
			rows, err := d.List(ctx,
				limit,
				req.GetString("type", ""),
				req.GetString("topic", ""),
				req.GetString("person", ""),
				req.GetInt("days", 0),
			)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.MarshalIndent(rows, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("thought_stats",
			mcp.WithDescription("Summarize the captured corpus: total rows, date range, and top types, topics, and people."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			stats, err := d.Stats(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.MarshalIndent(stats, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("search_thoughts",
			mcp.WithDescription("Search captured thoughts by meaning."),
			mcp.WithString("query", mcp.Required(),
				mcp.Description("Natural-language query; matches by semantic similarity.")),
			mcp.WithNumber("limit",
				mcp.Description("Max rows to return (default 10).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			limit := req.GetInt("limit", 10)

			emb, err := e.Embed(ctx, q)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rows, err := d.Search(ctx, emb, limit, "", nil)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.MarshalIndent(rows, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)
}

func runHealthcheck() error {
	scheme := "http"
	client := http.DefaultClient
	if os.Getenv("TLS_CERT_FILE") != "" && os.Getenv("TLS_KEY_FILE") != "" {
		scheme = "https"
		// Loopback: don't care about MITM; the distroless container has no CA store anyway.
		client = &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}}
	}
	res, err := client.Get(scheme + "://127.0.0.1:8080/health")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}
