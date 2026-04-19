package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

type DB struct{ Pool *pgxpool.Pool }

// Open builds a pgxpool with an AfterConnect hook that registers the pgvector
// types on every new connection. Without this hook, inserts that bind a
// pgvector.Vector parameter fail with an encoding error.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &DB{Pool: pool}, nil
}

func (d *DB) Close() { d.Pool.Close() }

func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

type Thought struct {
	ID          string   `json:"id"`
	Content     string   `json:"content"`
	ThoughtType string   `json:"thought_type"`
	Topics      []string `json:"topics"`
	People      []string `json:"people"`
	CreatedAt   string   `json:"created_at"`
	Similarity  float64  `json:"similarity"`
}

func (d *DB) Insert(ctx context.Context, content, thoughtType, source,
	provider, model string, topics, people []string, emb []float32,
) (string, error) {
	if topics == nil {
		topics = []string{}
	}
	if people == nil {
		people = []string{}
	}
	var id string
	err := d.Pool.QueryRow(ctx, `
        INSERT INTO thoughts
          (content, embedding, thought_type, topics, people, source, embed_provider, embed_model)
        VALUES ($1, $2, NULLIF($3,''), $4, $5, NULLIF($6,''), $7, $8)
        RETURNING id`,
		content, pgvector.NewVector(emb), thoughtType, topics, people, source, provider, model,
	).Scan(&id)
	return id, err
}

func (d *DB) Search(ctx context.Context, qEmb []float32, limit int,
	filterType string, filterTopics []string,
) ([]Thought, error) {
	rows, err := d.Pool.Query(ctx, `
        SELECT id, content, COALESCE(thought_type,''), topics, people,
               created_at::text, similarity
        FROM match_thoughts($1, $2, NULLIF($3,''), $4)`,
		pgvector.NewVector(qEmb), limit, filterType, filterTopics,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thought
	for rows.Next() {
		var t Thought
		if err := rows.Scan(&t.ID, &t.Content, &t.ThoughtType, &t.Topics,
			&t.People, &t.CreatedAt, &t.Similarity); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SchemaDim returns the pgvector dimension declared on thoughts.embedding.
func (d *DB) SchemaDim(ctx context.Context) (int, error) {
	var formatted string
	err := d.Pool.QueryRow(ctx, `
        SELECT format_type(atttypid, atttypmod) FROM pg_attribute
        WHERE attrelid = 'thoughts'::regclass AND attname = 'embedding'
    `).Scan(&formatted)
	if err != nil {
		return 0, err
	}
	var dim int
	if _, err := fmt.Sscanf(formatted, "vector(%d)", &dim); err != nil {
		return 0, fmt.Errorf("parse %q: %w", formatted, err)
	}
	return dim, nil
}

// LastRowProvider returns (provider, model) of the most recent row, or
// "", "", nil if the table is empty.
func (d *DB) LastRowProvider(ctx context.Context) (string, string, error) {
	var p, m string
	err := d.Pool.QueryRow(ctx, `
        SELECT embed_provider, embed_model FROM thoughts
        ORDER BY created_at DESC LIMIT 1`).Scan(&p, &m)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return p, m, err
}
