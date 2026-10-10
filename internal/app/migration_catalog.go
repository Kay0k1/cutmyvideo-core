package app

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Only catalog metadata is read. Startup never scans media, jobs or the storage
// ledger to validate a schema, so cost is independent of retained data size.
// All relations/functions are resolved in the single migration namespace.
const schemaShapeSQL = `
WITH managed AS (
 SELECT c.oid,c.relname,c.relkind FROM pg_class c
 JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=current_schema() AND c.relname=ANY($1::text[]) AND c.relkind IN ('r','p','v','m','f')
)
SELECT jsonb_build_object(
 'Tables',COALESCE((SELECT jsonb_object_agg(c.relname,jsonb_build_object(
  'Kind',c.relkind::text,
  'Columns',(SELECT jsonb_object_agg(a.attname,jsonb_build_object(
   'Type',format_type(a.atttypid,a.atttypmod),'NotNull',a.attnotnull,
   'Default',COALESCE(pg_get_expr(d.adbin,d.adrelid,true),''),
   'Identity',a.attidentity::text,'Generated',a.attgenerated::text))
   FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
   WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),
  'Constraints',COALESCE((SELECT jsonb_agg(pg_get_constraintdef(k.oid,true) ORDER BY pg_get_constraintdef(k.oid,true))
   FROM pg_constraint k WHERE k.conrelid=c.oid AND k.contype<>'n'),'[]'::jsonb),
  'Indexes',COALESCE((SELECT jsonb_object_agg(ic.relname,jsonb_build_object(
   'Unique',i.indisunique,'Valid',i.indisvalid,'Ready',i.indisready,
   'Columns',ARRAY(SELECT pg_get_indexdef(i.indexrelid,position,true) FROM generate_series(1,i.indnatts) position),
   'Predicate',COALESCE(pg_get_expr(i.indpred,i.indrelid,true),'')))
   FROM pg_index i JOIN pg_class ic ON ic.oid=i.indexrelid WHERE i.indrelid=c.oid),'{}'::jsonb),
  'Triggers',COALESCE((SELECT jsonb_object_agg(t.tgname,jsonb_build_object(
   'Function',pn.nspname || '.' || p.proname,'Type',t.tgtype,
   'Enabled',t.tgenabled::text,'OldTable',COALESCE(t.tgoldtable,''),'NewTable',COALESCE(t.tgnewtable,'')))
   FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace pn ON pn.oid=p.pronamespace
   WHERE t.tgrelid=c.oid AND NOT t.tgisinternal),'{}'::jsonb)
 )) FROM managed c),'{}'::jsonb),
 'Functions',COALESCE((SELECT jsonb_object_agg(p.proname,
  concat_ws(E'\n',l.lanname,p.prorettype::regtype::text,p.provolatile::text,p.prosecdef::text,COALESCE(p.proconfig::text,''),p.prosrc))
  FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_language l ON l.oid=p.prolang
  WHERE n.nspname=current_schema() AND p.proname='update_storage_counter' AND p.pronargs=0),'{}'::jsonb)
)`

func readSchemaShape(ctx context.Context, tx pgx.Tx) (schemaShape, error) {
	names := make([]string, 0, len(latestSchemaShape().Tables))
	for name := range latestSchemaShape().Tables {
		names = append(names, name)
	}
	slices.Sort(names)
	var payload []byte
	if err := tx.QueryRow(ctx, schemaShapeSQL, names).Scan(&payload); err != nil {
		return schemaShape{}, err
	}
	var shape schemaShape
	err := json.Unmarshal(payload, &shape)
	if err != nil {
		return shape, err
	}
	// Catalog trigger targets otherwise include the fixture/installation schema
	// name. Normalize only this verified namespace, never an external function.
	var namespace string
	if err = tx.QueryRow(ctx, "SELECT current_schema()").Scan(&namespace); err != nil {
		return shape, err
	}
	for name, table := range shape.Tables {
		for key, trigger := range table.Triggers {
			if trigger.Function == namespace+".update_storage_counter" {
				trigger.Function = "update_storage_counter"
				table.Triggers[key] = trigger
			}
		}
		shape.Tables[name] = table
	}
	return shape, nil
}
