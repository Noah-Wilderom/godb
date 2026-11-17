package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
	"uuid"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/mysqlwire"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/replication"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

type MySQLConfig struct {
	Address, User, PasswordFile, Database string
	MaxConnections                        int
	QueryTimeout, IdleTimeout             time.Duration
}

func (s *Server) openMySQL(ctx context.Context, certificate tls.Certificate) error {
	config := s.config.MySQL
	if config.Address == "" {
		return nil
	}

	if config.User == "" || config.PasswordFile == "" {
		return fmt.Errorf("%w: MySQL user and password file are required", ErrInvalidConfig)
	}

	file, err := os.Open(config.PasswordFile) // #nosec G304 -- Credential file explicitly selected by the local server operator.
	if err != nil {
		return err
	}

	raw, err := io.ReadAll(io.LimitReader(file, 4099))

	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}

	defer clear(raw)
	password := bytes.TrimSuffix(raw, []byte("\n"))

	password = bytes.TrimSuffix(password, []byte("\r"))
	if len(raw) > 4098 || len(password) == 0 || len(password) > 4096 || bytes.IndexByte(password, 0) >= 0 {
		return fmt.Errorf("%w: invalid MySQL password file", ErrInvalidConfig)
	}

	if config.Database == "" {
		config.Database = "godb"
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", config.Address)
	if err != nil {
		return err
	}

	endpoint, err := mysqlwire.New(listener, mysqlwire.Config{
		TLS:  &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}},
		User: config.User, Database: config.Database, PasswordHash: sha256.Sum256(password),
		MaxConnections: config.MaxConnections, QueryTimeout: config.QueryTimeout, IdleTimeout: config.IdleTimeout,
	}, mysqlwire.Handler{Query: s.queryMySQL, Describe: s.describeMySQL, Catalog: s.mysqlCatalog})
	if err != nil {
		return errors.Join(err, listener.Close())
	}

	s.mysql = endpoint
	return nil
}

func (s *Server) MySQLAddress() string {
	if s.mysql == nil {
		return ""
	}

	return s.mysql.Address()
}

func (s *Server) queryMySQL(ctx context.Context, query string, args ...record.Value) (sql.Result, error) {
	plan, err := s.PrepareSQL(ctx, query, args...)
	if err != nil {
		return sql.Result{}, s.mysqlError(err)
	}

	result, err := s.ExecuteSQL(ctx, uuid.NewV4(), plan)
	if err != nil {
		return sql.Result{}, s.mysqlError(err)
	}

	return result, nil
}

func (s *Server) mysqlError(err error) error {
	var rejected *sql.CommandError

	if errors.As(err, &rejected) {
		switch rejected.Code {
		case database.CommandDuplicatePrimaryKey, database.CommandUniqueConflict:
			return &mysqlwire.Error{Code: 1062, State: "23000", Message: "Duplicate key"}
		case database.CommandTableIDExists, database.CommandTableNameExists:
			return &mysqlwire.Error{Code: 1050, State: "42S01", Message: "Table name or ID already exists"}
		default:
			return &mysqlwire.Error{Code: 1105, State: "HY000", Message: "SQL command rejected"}
		}
	}

	switch {
	case errors.Is(err, replication.ErrNotLeader):
		message := "This member is not the leader"
		if status, statusErr := s.Status(); statusErr == nil && status.LeaderID != 0 {
			message = fmt.Sprintf("This member is not the leader; connect to node %d", status.LeaderID)
		}

		return &mysqlwire.Error{Code: 1290, State: "HY000", Message: message}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &mysqlwire.Error{Code: 3024, State: "HY000", Message: "Query interrupted; a submitted write may still commit"}
	case errors.Is(err, sql.ErrSyntax):
		return &mysqlwire.Error{Code: 1064, State: "42000", Message: "Invalid or unsupported SQL syntax"}
	case errors.Is(err, sql.ErrLimit):
		return &mysqlwire.Error{Code: 1105, State: "HY000", Message: "SQL resource limit exceeded"}
	case errors.Is(err, sql.ErrUnsupported):
		return &mysqlwire.Error{Code: 1235, State: "42000", Message: "SQL operation is not supported"}
	case errors.Is(err, sql.ErrIndexName):
		return &mysqlwire.Error{Code: 1061, State: "42000", Message: "Invalid or duplicate index name"}
	case errors.Is(err, sql.ErrColumn):
		return &mysqlwire.Error{Code: 1054, State: "42S22", Message: "Unknown or repeated column"}
	case errors.Is(err, sql.ErrType), errors.Is(err, sql.ErrBinding):
		return &mysqlwire.Error{Code: 1366, State: "HY000", Message: "Invalid SQL value or parameter"}
	case errors.Is(err, database.ErrTableNotFound):
		return &mysqlwire.Error{Code: 1146, State: "42S02", Message: "Table does not exist"}
	case errors.Is(err, ErrBusy):
		return &mysqlwire.Error{Code: 1040, State: "08004", Message: "Server request capacity reached"}
	case errors.Is(err, ErrClosed):
		return &mysqlwire.Error{Code: 1053, State: "08S01", Message: "Server is shutting down; a submitted write may still commit"}
	default:
		return &mysqlwire.Error{Code: 1105, State: "HY000", Message: "GoDB request failed"}
	}
}

func (s *Server) describeMySQL(ctx context.Context, query string) (sql.Description, error) {
	start := time.Now()
	ctx, span := s.tracer.Start(ctx, "godb.sql.describe")
	value, err := s.linearRead(ctx, func() (any, error) { return sql.Describe(s.db, query) })

	outcome := "ok"
	if err != nil {
		outcome = "error"
	}

	s.metrics.sqlPrepare.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
	finishSpan(span, outcome, err != nil)

	if err != nil {
		return sql.Description{}, s.mysqlError(err)
	}

	return value.(sql.Description), nil
}

func (s *Server) mysqlCatalog(ctx context.Context, name string) ([]schema.Table, error) {
	start := time.Now()
	ctx, span := s.tracer.Start(ctx, "godb.mysql.catalog")
	value, err := s.linearRead(ctx, func() (any, error) {
		if name != "" {
			table, found, err := s.db.GetTableByName(name)
			if err != nil {
				return nil, err
			}

			if !found {
				return nil, database.ErrTableNotFound
			}

			return []schema.Table{table}, nil
		}

		var tables []schema.Table

		after := ""

		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			page, err := s.db.ListTables(after, 256)
			if err != nil {
				return nil, err
			}

			tables = append(tables, page...)
			if len(tables) > mysqlwire.MaxCatalogTables {
				return nil, sql.ErrLimit
			}

			if len(page) < 256 {
				return tables, nil
			}

			after = page[len(page)-1].Name
		}
	})

	outcome := "ok"
	if err != nil {
		outcome = "error"
	}

	s.metrics.mysqlCatalog.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
	finishSpan(span, outcome, err != nil)

	if err != nil {
		return nil, s.mysqlError(err)
	}

	return value.([]schema.Table), nil
}
