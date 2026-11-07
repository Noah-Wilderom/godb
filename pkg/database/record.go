package database

import (
	"fmt"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
)

func (db *Database) PutValue(key []byte, value recordpkg.Value) error {
	encoded, err := recordpkg.EncodeValue(value)
	if err != nil {
		return fmt.Errorf("encode value: %w", err)
	}

	return db.Put(key, encoded)
}

func (db *Database) GetValue(key []byte) (recordpkg.Value, bool, error) {
	encoded, found, err := db.Get(key)
	if err != nil {
		return recordpkg.Value{}, false, err
	}

	if !found {
		return recordpkg.Value{}, false, nil
	}

	value, err := recordpkg.DecodeValue(encoded)
	if err != nil {
		return recordpkg.Value{}, false, fmt.Errorf("decode value: %w", err)
	}

	return value, true, nil
}
