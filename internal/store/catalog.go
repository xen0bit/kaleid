package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/collection"
	"github.com/xen0bit/kaleid/internal/wire"
)

// Tenant is a tenant row.
type Tenant struct {
	Name         string
	ResourceName *string
}

// Database is a database row.
type Database struct {
	ID     uuid.UUID
	Name   string
	Tenant string
}

// IndexState records the ANN index built on a collection's vector column.
type IndexState struct {
	Kind           string `json:"kind"` // vector | halfvec | none
	M              int    `json:"m,omitempty"`
	EfConstruction int    `json:"ef_construction,omitempty"`
}

// Collection is a collection row plus its resolved tenant/database names.
type Collection struct {
	ID         uuid.UUID
	Name       string
	Tenant     string
	Database   string
	DatabaseID uuid.UUID
	Metadata   wire.Metadata
	Schema     *collection.Schema
	Dimension  *int
	Index      *IndexState
	Version    int
}

// ---------------------------------------------------------------------------
// Tenants
// ---------------------------------------------------------------------------

// CreateTenant creates a tenant.
func (s *Store) CreateTenant(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO kaleid.tenants (name) VALUES ($1)`, name)
	if isUniqueViolation(err) {
		return apierr.AlreadyExists("Tenant [%s] already exists", name)
	}
	return err
}

// GetTenant fetches a tenant.
func (s *Store) GetTenant(ctx context.Context, name string) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx, `SELECT name, resource_name FROM kaleid.tenants WHERE name=$1`, name).Scan(&t.Name, &t.ResourceName)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, apierr.NotFound("Tenant [%s] not found", name)
	}
	return t, err
}

// UpdateTenant sets a tenant's resource name.
func (s *Store) UpdateTenant(ctx context.Context, name, resourceName string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE kaleid.tenants SET resource_name=$2 WHERE name=$1`, name, resourceName)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apierr.NotFound("Tenant [%s] not found", name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------

// CreateDatabase creates a database in a tenant.
func (s *Store) CreateDatabase(ctx context.Context, tenant, name string) error {
	// Local Chroma does not require the tenant to exist.
	_, err := s.pool.Exec(ctx, `INSERT INTO kaleid.databases (id, tenant, name) VALUES ($1,$2,$3)`, uuid.New(), tenant, name)
	if isUniqueViolation(err) {
		return apierr.AlreadyExists("Database [%s] already exists", name)
	}
	return err
}

// ListDatabases lists a tenant's databases ordered by name.
func (s *Store) ListDatabases(ctx context.Context, tenant string, limit *int, offset int) ([]Database, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, tenant FROM kaleid.databases WHERE tenant=$1 ORDER BY name LIMIT $2 OFFSET $3`,
		tenant, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Database, error) {
		var d Database
		err := r.Scan(&d.ID, &d.Name, &d.Tenant)
		return d, err
	})
}

// GetDatabase fetches a database by name.
func (s *Store) GetDatabase(ctx context.Context, tenant, name string) (Database, error) {
	var d Database
	err := s.pool.QueryRow(ctx, `SELECT id, name, tenant FROM kaleid.databases WHERE tenant=$1 AND name=$2`, tenant, name).
		Scan(&d.ID, &d.Name, &d.Tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, apierr.NotFound("Database [%s] not found. Are you sure it exists?", name)
	}
	return d, err
}

// GetDatabaseByID fetches a database by id.
func (s *Store) GetDatabaseByID(ctx context.Context, tenant string, id uuid.UUID) (Database, error) {
	var d Database
	err := s.pool.QueryRow(ctx, `SELECT id, name, tenant FROM kaleid.databases WHERE tenant=$1 AND id=$2`, tenant, id).
		Scan(&d.ID, &d.Name, &d.Tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, apierr.NotFound("Database [%s] not found", id)
	}
	return d, err
}

// DeleteDatabase deletes a database and all of its collections.
func (s *Store) DeleteDatabase(ctx context.Context, tenant, name string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM kaleid.databases WHERE tenant=$1 AND name=$2 FOR UPDATE`, tenant, name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("Database [%s] not found", name)
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM kaleid.collections WHERE database_id=$1`, id)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		for _, cid := range ids {
			if err := dropCollectionTables(ctx, tx, cid); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM kaleid.databases WHERE id=$1`, id)
		return err
	})
}

// ---------------------------------------------------------------------------
// Collections
// ---------------------------------------------------------------------------

const collectionCols = `c.id, c.name, d.tenant, d.name, c.database_id, c.metadata, c.schema, c.dimension, c.index_state, c.version`

func scanCollection(row pgx.Row) (Collection, error) {
	var c Collection
	var md, schema, idx []byte
	if err := row.Scan(&c.ID, &c.Name, &c.Tenant, &c.Database, &c.DatabaseID, &md, &schema, &c.Dimension, &idx, &c.Version); err != nil {
		return c, err
	}
	var err error
	if c.Metadata, err = wire.ParseMetadata(md); err != nil {
		return c, fmt.Errorf("corrupt collection metadata: %w", err)
	}
	c.Schema = &collection.Schema{}
	if err := json.Unmarshal(schema, c.Schema); err != nil {
		return c, fmt.Errorf("corrupt collection schema: %w", err)
	}
	if c.Schema.Keys == nil {
		c.Schema.Keys = map[string]*collection.ValueTypes{}
	}
	if len(idx) > 0 {
		c.Index = &IndexState{}
		if err := json.Unmarshal(idx, c.Index); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (s *Store) resolveDatabase(ctx context.Context, q pgx.Row, tenant, db string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, apierr.NotFound("Database [%s] does not exist", db)
	}
	return id, err
}

// CreateCollectionParams are the inputs to CreateCollection.
type CreateCollectionParams struct {
	Tenant, Database, Name string
	Metadata               wire.Metadata
	Schema                 *collection.Schema
	GetOrCreate            bool
}

// CreateCollection creates (or with GetOrCreate, fetches) a collection.
func (s *Store) CreateCollection(ctx context.Context, p CreateCollectionParams) (Collection, error) {
	var out Collection
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		dbID, err := s.resolveDatabase(ctx, tx.QueryRow(ctx,
			`SELECT id FROM kaleid.databases WHERE tenant=$1 AND name=$2`, p.Tenant, p.Database), p.Tenant, p.Database)
		if err != nil {
			return err
		}
		// Serialize creates of the same name so get_or_create is race free.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, dbID.String()+"/"+p.Name); err != nil {
			return err
		}
		existing, err := scanCollection(tx.QueryRow(ctx, `SELECT `+collectionCols+` FROM kaleid.collections c
			JOIN kaleid.databases d ON d.id=c.database_id WHERE c.database_id=$1 AND c.name=$2`, dbID, p.Name))
		if err == nil {
			if p.GetOrCreate {
				out = existing
				return nil
			}
			return apierr.AlreadyExists("Collection [%s] already exists", p.Name)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := uuid.New()
		schemaJSON, err := json.Marshal(p.Schema)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO kaleid.collections (id, database_id, name, metadata, schema) VALUES ($1,$2,$3,$4,$5)`,
			id, dbID, p.Name, nullableJSON(p.Metadata), schemaJSON); err != nil {
			if isUniqueViolation(err) {
				return apierr.AlreadyExists("Collection [%s] already exists", p.Name)
			}
			return err
		}
		if err := createCollectionTables(ctx, tx, id); err != nil {
			return err
		}
		out = Collection{ID: id, Name: p.Name, Tenant: p.Tenant, Database: p.Database, DatabaseID: dbID,
			Metadata: p.Metadata, Schema: p.Schema}
		return nil
	})
	return out, err
}

func nullableJSON(md wire.Metadata) []byte {
	if md == nil {
		return nil
	}
	return md.StorageJSON()
}

// ListCollections lists collections in a database ordered by id.
func (s *Store) ListCollections(ctx context.Context, tenant, db string, limit *int, offset int) ([]Collection, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+collectionCols+` FROM kaleid.collections c
		JOIN kaleid.databases d ON d.id=c.database_id
		WHERE d.tenant=$1 AND d.name=$2 ORDER BY c.id::text LIMIT $3 OFFSET $4`, tenant, db, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountCollections counts collections in a database.
func (s *Store) CountCollections(ctx context.Context, tenant, db string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM kaleid.collections c JOIN kaleid.databases d ON d.id=c.database_id
		WHERE d.tenant=$1 AND d.name=$2`, tenant, db).Scan(&n)
	return n, err
}

// GetCollectionByName fetches a collection by name.
func (s *Store) GetCollectionByName(ctx context.Context, tenant, db, name string) (Collection, error) {
	c, err := scanCollection(s.pool.QueryRow(ctx, `SELECT `+collectionCols+` FROM kaleid.collections c
		JOIN kaleid.databases d ON d.id=c.database_id WHERE d.tenant=$1 AND d.name=$2 AND c.name=$3`, tenant, db, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apierr.NotFound("Collection [%s] does not exist", name)
	}
	return c, err
}

// GetCollectionByID fetches a collection by id within a tenant/database.
func (s *Store) GetCollectionByID(ctx context.Context, tenant, db string, id uuid.UUID) (Collection, error) {
	c, err := scanCollection(s.pool.QueryRow(ctx, `SELECT `+collectionCols+` FROM kaleid.collections c
		JOIN kaleid.databases d ON d.id=c.database_id WHERE d.tenant=$1 AND d.name=$2 AND c.id=$3`, tenant, db, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apierr.NotFound("Collection [%s] does not exist", id)
	}
	return c, err
}

// GetCollectionAnyDB fetches a collection by id in any database of a tenant.
func (s *Store) GetCollectionAnyDB(ctx context.Context, tenant string, id uuid.UUID) (Collection, error) {
	c, err := scanCollection(s.pool.QueryRow(ctx, `SELECT `+collectionCols+` FROM kaleid.collections c
		JOIN kaleid.databases d ON d.id=c.database_id WHERE d.tenant=$1 AND c.id=$2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apierr.NotFound("Collection [%s] does not exist", id)
	}
	return c, err
}

// UpdateCollectionParams describe a collection modification.
type UpdateCollectionParams struct {
	NewName       *string
	NewMetadata   wire.Metadata // nil = unchanged
	SetMetadata   bool
	Configuration *collection.UpdateConfiguration
}

// UpdateCollection renames and/or replaces metadata and/or updates config.
func (s *Store) UpdateCollection(ctx context.Context, tenant, db string, id uuid.UUID, p UpdateCollectionParams) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey(id)); err != nil {
			return err
		}
		c, err := scanCollection(tx.QueryRow(ctx, `SELECT `+collectionCols+` FROM kaleid.collections c
			JOIN kaleid.databases d ON d.id=c.database_id WHERE d.tenant=$1 AND d.name=$2 AND c.id=$3 FOR UPDATE OF c`, tenant, db, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("Collection [%s] does not exist", id)
		}
		if err != nil {
			return err
		}
		if p.NewName != nil && *p.NewName != c.Name {
			if _, err := tx.Exec(ctx, `UPDATE kaleid.collections SET name=$2 WHERE id=$1`, id, *p.NewName); err != nil {
				if isUniqueViolation(err) {
					return apierr.AlreadyExists("Collection [%s] already exists", *p.NewName)
				}
				return err
			}
		}
		if p.SetMetadata {
			if _, err := tx.Exec(ctx, `UPDATE kaleid.collections SET metadata=$2 WHERE id=$1`, id, nullableJSON(p.NewMetadata)); err != nil {
				return err
			}
		}
		if p.Configuration != nil {
			c.Schema.ApplyUpdate(p.Configuration)
			b, err := json.Marshal(c.Schema)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE kaleid.collections SET schema=$2 WHERE id=$1`, id, b); err != nil {
				return err
			}
			// Rebuild the ANN index if its build parameters changed.
			if c.Dimension != nil && c.Index != nil && (c.Index.Kind == "vector" || c.Index.Kind == "halfvec") {
				want := planIndex(*c.Dimension, c.Schema.ToInternal().Hnsw)
				if want.M != c.Index.M || want.EfConstruction != c.Index.EfConstruction {
					if err := s.buildIndex(ctx, tx, c.ID, *c.Dimension, c.Schema.ToInternal().Hnsw); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// DeleteCollection deletes a collection (by name) and drops its tables.
func (s *Store) DeleteCollection(ctx context.Context, tenant, db, name string) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT c.id FROM kaleid.collections c JOIN kaleid.databases d ON d.id=c.database_id
			WHERE d.tenant=$1 AND d.name=$2 AND c.name=$3 FOR UPDATE OF c`, tenant, db, name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("Collection [%s] does not exist", name)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey(id)); err != nil {
			return err
		}
		if err := dropCollectionTables(ctx, tx, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM kaleid.collections WHERE id=$1`, id)
		return err
	})
}

// Reset drops every collection, database and tenant, then re-seeds the
// defaults.
func (s *Store) Reset(ctx context.Context) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DROP SCHEMA kaleid_data CASCADE; CREATE SCHEMA kaleid_data;
			DELETE FROM kaleid.collections; DELETE FROM kaleid.databases; DELETE FROM kaleid.tenants;
			INSERT INTO kaleid.tenants (name) VALUES ('default_tenant');
			INSERT INTO kaleid.databases (id, tenant, name) VALUES ('00000000-0000-0000-0000-000000000000', 'default_tenant', 'default_database')`); err != nil {
			return err
		}
		return nil
	})
}
