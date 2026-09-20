package store

import (
	"database/sql"
	"errors"
)

func requireOneRow(result sql.Result, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New(message)
	}
	return nil
}
