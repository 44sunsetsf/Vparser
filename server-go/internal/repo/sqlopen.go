package repo

import (
	"database/sql"

	"github.com/go-sql-driver/mysql"
)

func openSQL(cfg *mysql.Config) (*sql.DB, error) {
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}
