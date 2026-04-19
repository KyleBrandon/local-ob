CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS thoughts (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  content         TEXT NOT NULL,
  embedding       vector(768) NOT NULL,
  thought_type    TEXT,
  topics          TEXT[] NOT NULL DEFAULT '{}',
  people          TEXT[] NOT NULL DEFAULT '{}',
  source          TEXT,
  embed_provider  TEXT NOT NULL,
  embed_model     TEXT NOT NULL,
  metadata        JSONB NOT NULL DEFAULT '{}',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS thoughts_embedding_idx
  ON thoughts USING hnsw (embedding vector_cosine_ops)
  WITH (m = 16, ef_construction = 64);

CREATE INDEX IF NOT EXISTS thoughts_topics_idx   ON thoughts USING GIN (topics);
CREATE INDEX IF NOT EXISTS thoughts_people_idx   ON thoughts USING GIN (people);
CREATE INDEX IF NOT EXISTS thoughts_content_trgm ON thoughts USING GIN (content gin_trgm_ops);
CREATE INDEX IF NOT EXISTS thoughts_created_idx  ON thoughts (created_at DESC);
CREATE INDEX IF NOT EXISTS thoughts_provider_idx ON thoughts (embed_provider, embed_model);

CREATE OR REPLACE FUNCTION match_thoughts(
  query_embedding vector(768),
  match_count     int DEFAULT 10,
  filter_type     text DEFAULT NULL,
  filter_topics   text[] DEFAULT NULL,
  filter_people   text[] DEFAULT NULL,
  after_date      timestamptz DEFAULT NULL
) RETURNS TABLE (
  id UUID, content TEXT, thought_type TEXT,
  topics TEXT[], people TEXT[], created_at TIMESTAMPTZ, similarity float
) LANGUAGE sql STABLE AS $$
  SELECT t.id, t.content, t.thought_type, t.topics, t.people, t.created_at,
         1 - (t.embedding <=> query_embedding) AS similarity
  FROM thoughts t
  WHERE
    (filter_type   IS NULL OR t.thought_type = filter_type)
    AND (filter_topics IS NULL OR t.topics && filter_topics)
    AND (filter_people IS NULL OR t.people && filter_people)
    AND (after_date   IS NULL OR t.created_at >= after_date)
  ORDER BY t.embedding <=> query_embedding
  LIMIT match_count;
$$;

CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS thoughts_touch ON thoughts;
CREATE TRIGGER thoughts_touch BEFORE UPDATE ON thoughts
  FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
