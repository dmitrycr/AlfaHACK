package migrations

import _ "embed"

//go:embed 001_auth.sql
var SQL string
