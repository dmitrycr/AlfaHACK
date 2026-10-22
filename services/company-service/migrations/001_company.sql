CREATE TABLE IF NOT EXISTS company.companies (
 id text PRIMARY KEY, document jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS company.profiles (
 company_id text NOT NULL REFERENCES company.companies(id), version integer NOT NULL CHECK(version>0),
 document jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(company_id,version)
);
