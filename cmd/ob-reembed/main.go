package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"github.com/KyleBrandon/local-ob/internal/embed"
)

func main() {
	_ = godotenv.Load()
	ctx := context.Background()

	dim, _ := strconv.Atoi(os.Getenv("EMBED_DIMENSION"))
	p, err := embed.New(embed.Config{
		Provider:  os.Getenv("EMBED_PROVIDER"),
		Model:     os.Getenv("EMBED_MODEL"),
		Dimension: dim,
		APIKey:    os.Getenv("EMBED_API_KEY"),
		BaseURL:   os.Getenv("EMBED_BASE_URL"),
	})
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
        SELECT id::text, content FROM thoughts
        WHERE embed_provider != $1 OR embed_model != $2
        ORDER BY created_at`, p.Name(), p.Model())
	if err != nil {
		log.Fatalf("query: %v", err)
	}

	type item struct{ ID, Content string }
	var batch []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Content); err != nil {
			log.Fatalf("scan: %v", err)
		}
		batch = append(batch, it)
	}
	rows.Close()
	log.Printf("re-embedding %d rows with %s/%s", len(batch), p.Name(), p.Model())

	const bs = 64
	for i := 0; i < len(batch); i += bs {
		end := i + bs
		if end > len(batch) {
			end = len(batch)
		}
		chunk := batch[i:end]
		texts := make([]string, len(chunk))
		for j, c := range chunk {
			texts[j] = c.Content
		}
		embs, err := p.EmbedBatch(ctx, texts)
		if err != nil {
			log.Fatalf("embed batch %d: %v", i, err)
		}
		for j, e := range embs {
			if _, err := pool.Exec(ctx, `
                UPDATE thoughts SET embedding=$1, embed_provider=$2, embed_model=$3
                WHERE id=$4`,
				pgvector.NewVector(e), p.Name(), p.Model(), chunk[j].ID); err != nil {
				log.Fatalf("update: %v", err)
			}
		}
		log.Printf("  %d/%d", end, len(batch))
	}
	log.Println("done")
}
