WITH source_retention_candidates AS MATERIALIZED (
 SELECT source.id FROM sources source
 WHERE source.retention_pins_valid AND source.retention_job_pins=0 AND source.retention_preview_pins=0
 AND source.created_at<now()-($1*interval '1 second')
 ORDER BY source.created_at,source.id LIMIT $2
), checked AS MATERIALIZED (
 SELECT candidate.id,
 EXISTS(SELECT 1 FROM jobs WHERE source_id=candidate.id OFFSET 0)
 OR EXISTS(SELECT 1 FROM storage_reservations WHERE kind='preview' AND job_id=candidate.id) AS pinned
 FROM source_retention_candidates candidate
), quarantined AS (
 UPDATE sources source SET retention_pins_valid=false
 WHERE source.id=ANY(ARRAY(SELECT id FROM checked WHERE pinned)) RETURNING source.id
), deleted AS (
 DELETE FROM sources source WHERE source.id=ANY(ARRAY(SELECT id FROM checked WHERE NOT pinned))
 RETURNING source.path,source.thumbnail_path,source.owner
)
SELECT 'deleted'::text,path,thumbnail_path,owner,0::bigint,0::bigint FROM deleted
UNION ALL
SELECT 'progress'::text,'','','',(SELECT count(*) FROM source_retention_candidates),(SELECT count(*) FROM quarantined)