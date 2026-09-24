// Package migrations embeds the SQL schema files consumed by golang-migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
