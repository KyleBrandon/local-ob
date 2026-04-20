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

// List returns recent thoughts filtered by optional type/topic/person and an
// optional recency window in days. Empty-string filters are ignored; days <= 0
// disables the recency filter. Results are ordered newest first.
func (d *DB) List(ctx context.Context, limit int, thoughtType, topic, person string, days int,
) ([]Thought, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.Pool.Query(ctx, `
        SELECT id, content, COALESCE(thought_type,''), topics, people, created_at::text
        FROM thoughts
        WHERE (NULLIF($2,'') IS NULL OR thought_type = $2)
          AND (NULLIF($3,'') IS NULL OR $3 = ANY(topics))
          AND (NULLIF($4,'') IS NULL OR $4 = ANY(people))
          AND ($5 <= 0 OR created_at >= now() - make_interval(days => $5))
        ORDER BY created_at DESC
        LIMIT $1`,
		limit, thoughtType, topic, person, days,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thought
	for rows.Next() {
		var t Thought
		if err := rows.Scan(&t.ID, &t.Content, &t.ThoughtType, &t.Topics,
			&t.People, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type CountEntry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Stats struct {
	Total     int          `json:"total"`
	Oldest    string       `json:"oldest,omitempty"`
	Newest    string       `json:"newest,omitempty"`
	Types     []CountEntry `json:"types"`
	TopTopics []CountEntry `json:"top_topics"`
	TopPeople []CountEntry `json:"top_people"`
}

// Stats summarizes the corpus: total rows, date range, and the top types,
// topics, and people by frequency. Empty arrays unnest to zero rows, so the
// topic/people aggregations only count real values.
func (d *DB) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	s.Types = []CountEntry{}
	s.TopTopics = []CountEntry{}
	s.TopPeople = []CountEntry{}

	var oldest, newest *string
	if err := d.Pool.QueryRow(ctx, `
        SELECT count(*), min(created_at)::text, max(created_at)::text FROM thoughts
    `).Scan(&s.Total, &oldest, &newest); err != nil {
		return s, err
	}
	if oldest != nil {
		s.Oldest = *oldest
	}
	if newest != nil {
		s.Newest = *newest
	}
	if s.Total == 0 {
		return s, nil
	}

	collect := func(sql string) ([]CountEntry, error) {
		rows, err := d.Pool.Query(ctx, sql)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []CountEntry
		for rows.Next() {
			var e CountEntry
			if err := rows.Scan(&e.Name, &e.Count); err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, rows.Err()
	}

	types, err := collect(`
        SELECT COALESCE(NULLIF(thought_type,''), 'unknown') AS t, count(*)::int
        FROM thoughts GROUP BY t ORDER BY 2 DESC`)
	if err != nil {
		return s, fmt.Errorf("types: %w", err)
	}
	if types != nil {
		s.Types = types
	}

	topics, err := collect(`
        SELECT t, count(*)::int FROM thoughts, unnest(topics) t
        GROUP BY t ORDER BY 2 DESC LIMIT 10`)
	if err != nil {
		return s, fmt.Errorf("topics: %w", err)
	}
	if topics != nil {
		s.TopTopics = topics
	}

	people, err := collect(`
        SELECT p, count(*)::int FROM thoughts, unnest(people) p
        GROUP BY p ORDER BY 2 DESC LIMIT 10`)
	if err != nil {
		return s, fmt.Errorf("people: %w", err)
	}
	if people != nil {
		s.TopPeople = people
	}
	return s, nil
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
