ALTER TABLE auth.users
 ADD COLUMN full_name text NOT NULL DEFAULT '',
 ADD COLUMN job_title text NOT NULL DEFAULT '',
 ADD COLUMN email_verified boolean NOT NULL DEFAULT true,
 ADD COLUMN access_status text NOT NULL DEFAULT 'active'
 CHECK (access_status IN ('pending_email','pending_approval','active','blocked'));
CREATE TABLE auth.registration_requests (
 key_hash text PRIMARY KEY, fingerprint text NOT NULL,
 user_id text NOT NULL REFERENCES auth.users(id), company_check jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE auth.email_verifications (
 user_id text PRIMARY KEY REFERENCES auth.users(id), token_hash text NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL
);
-- Development-only delivery sink. Tokens are never returned by the public API.
CREATE TABLE auth.mock_mail (
 user_id text PRIMARY KEY REFERENCES auth.users(id), token text NOT NULL,
 expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE auth.membership_decisions (
 id bigserial PRIMARY KEY, user_id text NOT NULL REFERENCES auth.users(id),
 operator text NOT NULL, action text NOT NULL CHECK(action IN ('approve','revoke')),
 created_at timestamptz NOT NULL DEFAULT now()
);
