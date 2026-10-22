package migrations

import _ "embed"

//go:embed 001_analytics.sql
var SQL string
