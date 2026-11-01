-- Convert results to a TimescaleDB hypertable when the extension is installed
-- and preloaded (as in the timescale/timescaledb images). On plain PostgreSQL
-- this migration is a no-op and everything still works.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'timescaledb')
       AND current_setting('shared_preload_libraries', true) LIKE '%timescaledb%' THEN
        CREATE EXTENSION IF NOT EXISTS timescaledb;
        PERFORM create_hypertable('results', 'time',
                                  chunk_time_interval => INTERVAL '1 day',
                                  migrate_data => true,
                                  if_not_exists => true);
    END IF;
END
$$;
