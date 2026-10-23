ALTER TABLE company.companies ADD COLUMN inn text UNIQUE
 CHECK (inn IS NULL OR inn ~ '^[0-9]{10}$');
CREATE TABLE company.registry_checks (
 inn text PRIMARY KEY, company_id text NOT NULL REFERENCES company.companies(id),
 submitted_name text NOT NULL, canonical_name text NOT NULL,
 provider text NOT NULL, checked_at timestamptz NOT NULL
);
