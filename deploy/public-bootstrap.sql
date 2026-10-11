-- Fresh named volumes only. Bootstrap admin never enters API/worker containers.
\getenv migration_password CUTMY_MIGRATION_PASSWORD
\getenv application_password CUTMY_APP_PASSWORD
CREATE ROLE cutmy_migrate LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'migration_password';
CREATE ROLE cutmy_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'application_password';
ALTER DATABASE cutmy OWNER TO cutmy_migrate;
ALTER SCHEMA public OWNER TO cutmy_migrate;
REVOKE ALL ON DATABASE cutmy FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE cutmy TO cutmy_runtime;
GRANT USAGE ON SCHEMA public TO cutmy_runtime;
ALTER ROLE cutmy_runtime SET search_path TO public;
ALTER ROLE cutmy_migrate SET search_path TO public;
