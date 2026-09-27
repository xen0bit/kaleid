CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE SCHEMA IF NOT EXISTS kaleid_data;

CREATE TABLE kaleid.tenants (
    name          text PRIMARY KEY,
    resource_name text,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE kaleid.databases (
    id         uuid PRIMARY KEY,
    tenant     text NOT NULL,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant, name)
);

CREATE TABLE kaleid.collections (
    id           uuid PRIMARY KEY,
    database_id  uuid NOT NULL REFERENCES kaleid.databases(id) ON DELETE CASCADE,
    name         text NOT NULL,
    metadata     jsonb,
    schema       jsonb NOT NULL,
    dimension    integer,
    -- Index actually built on the vector column: NULL until the dimension is
    -- known. {"kind": "vector"|"halfvec"|"none", "m": .., "ef_construction": ..}
    index_state  jsonb,
    write_seq    bigint NOT NULL DEFAULT 0,
    version      integer NOT NULL DEFAULT 0,
    forked_from  uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (database_id, name)
);

CREATE INDEX collections_database_idx ON kaleid.collections (database_id, id);

INSERT INTO kaleid.tenants (name) VALUES ('default_tenant');
INSERT INTO kaleid.databases (id, tenant, name)
VALUES ('00000000-0000-0000-0000-000000000000', 'default_tenant', 'default_database');
