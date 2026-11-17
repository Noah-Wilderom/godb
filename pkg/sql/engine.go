package sql

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
	"uuid"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

type CommandError struct{ Code database.CommandResultCode }

func (e *CommandError) Error() string { return fmt.Sprintf("%s: %v", e.Code, ErrRejected) }
func (e *CommandError) Unwrap() error { return ErrRejected }

type LocalEngine struct {
	db      *database.Database
	ddl     sync.Mutex
	nextID  uint64
	metrics *metrics
}

func NewLocal(db *database.Database) (*LocalEngine, error) {
	if db == nil {
		return nil, errors.New("SQL requires a database")
	}

	if err := requireStandalone(db); err != nil {
		return nil, err
	}

	return &LocalEngine{db: db, nextID: 1, metrics: newMetrics()}, nil
}

func requireStandalone(db *database.Database) error {
	position, err := db.AppliedPosition()
	if err != nil {
		return err
	}

	if position.GroupID != uuid.Nil() {
		return ErrClusterExecution
	}

	return nil
}

func (e *LocalEngine) Metrics() prometheus.Collector { return e.metrics.group }

func (e *LocalEngine) Execute(ctx context.Context, query string, args ...record.Value) (result Result, err error) {
	start := time.Now()
	kind := "unknown"
	access := ""
	aggregate := ""
	e.metrics.active.Inc()
	defer func() {
		e.metrics.active.Dec()

		outcome := "ok"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = "canceled"
		} else if err != nil {
			outcome = "error"
		}

		if aggregate != "" {
			e.metrics.aggregates.WithLabelValues(aggregate, outcome).Inc()
		}

		if access != "" {
			e.metrics.selects.WithLabelValues(access, outcome).Inc()
		}

		e.metrics.duration.WithLabelValues(kind, outcome).Observe(time.Since(start).Seconds())

		if err == nil {
			e.metrics.rows.Add(float64(len(result.Rows)))
			e.metrics.affected.Add(float64(result.AffectedRows))

			if kind == string(Delete) {
				e.metrics.deleted.Add(float64(result.AffectedRows))
			}

			if kind == string(Update) {
				e.metrics.matched.Add(float64(result.MatchedRows))
			}
		}
	}()

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if err := requireStandalone(e.db); err != nil {
		return Result{}, err
	}

	s, err := Parse(query)
	if err != nil {
		return Result{}, err
	}

	kind = string(s.kind)
	options := PrepareOptions{}

	if s.kind == CreateTable {
		e.ddl.Lock()
		defer e.ddl.Unlock()

		for e.nextID <= math.MaxUint32 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}

			id := uint32(e.nextID)

			_, found, err := e.db.GetTable(id)
			if err != nil {
				return Result{}, err
			}

			e.nextID++

			if !found {
				options.CreateTableID = id
				break
			}
		}

		if options.CreateTableID == 0 {
			return Result{}, ErrTableID
		}
	}

	plan, err := prepare(e.db, s, options, args)
	if err != nil {
		return Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if plan.kind == Select || plan.kind == Explain {
		access = plan.AccessPath()
		aggregate = plan.Aggregate()
		return plan.Read(e.db)
	}

	outcome, err := e.db.ApplyCommand(plan.command)
	if err != nil {
		return Result{}, err
	}

	return plan.ApplyResult(outcome)
}
