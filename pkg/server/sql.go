package server

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"
	"uuid"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

var ErrSQLPlan = errors.New("SQL plan is missing or belongs to another Raft group")

type SQLPlan struct {
	group uuid.UUID
	plan  *sql.Plan
}

func (p *SQLPlan) Kind() sql.Kind { return p.plan.Kind() }

func (p *SQLPlan) Command() ([]byte, error) {
	if p == nil || p.plan == nil {
		return nil, ErrSQLPlan
	}

	return p.plan.Command()
}

func (s *Server) PrepareSQL(ctx context.Context, query string, args ...record.Value) (_ *SQLPlan, err error) {
	start := time.Now()
	ctx, span := s.tracer.Start(ctx, "godb.sql.prepare")
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}

		s.metrics.sqlPrepare.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
		finishSpan(span, outcome, err != nil)
	}()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	statement, err := sql.Parse(query)
	if err != nil {
		return nil, err
	}

	args = slices.Clone(args)

	value, err := s.linearRead(ctx, func() (any, error) {
		options := sql.PrepareOptions{}

		if statement.Kind() == sql.CreateTable {
			id, err := s.allocateSQLTableID(ctx)
			if err != nil {
				return nil, err
			}

			options.CreateTableID = id
		}

		plan, err := sql.Prepare(s.db, query, options, args...)
		if err != nil {
			return nil, err
		}

		return &SQLPlan{group: s.config.Identity.GroupID, plan: plan}, nil
	})
	if err != nil {
		return nil, err
	}

	return value.(*SQLPlan), nil
}

func (s *Server) allocateSQLTableID(ctx context.Context) (uint32, error) {
	for range maxPending {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		if s.nextSQLTableID > math.MaxUint32 {
			return 0, sql.ErrTableID
		}

		id := uint32(s.nextSQLTableID)
		s.nextSQLTableID++

		_, found, err := s.db.GetTable(id)
		if err != nil {
			return 0, err
		}

		if !found {
			return id, nil
		}
	}

	return 0, ErrBusy
}

func (s *Server) ExecuteSQL(ctx context.Context, id uuid.UUID, plan *SQLPlan) (result sql.Result, err error) {
	start := time.Now()
	kind := "unknown"
	access := ""
	aggregate := ""
	ctx, span := s.tracer.Start(ctx, "godb.sql.execute")
	s.metrics.sqlActive.Inc()
	defer func() {
		s.metrics.sqlActive.Dec()

		outcome := "ok"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = "canceled"
		} else if err != nil {
			outcome = "error"
		}

		if aggregate != "" {
			s.metrics.sqlAggregates.WithLabelValues(aggregate, outcome).Inc()
		}

		if access != "" {
			s.metrics.sqlSelects.WithLabelValues(access, outcome).Inc()
		}

		s.metrics.sqlDuration.WithLabelValues(kind, outcome).Observe(time.Since(start).Seconds())

		if err == nil {
			s.metrics.sqlRows.Add(float64(len(result.Rows)))
			s.metrics.sqlAffected.Add(float64(result.AffectedRows))

			if kind == string(sql.Delete) {
				s.metrics.sqlDeleted.Add(float64(result.AffectedRows))
			}

			if kind == string(sql.Update) {
				s.metrics.sqlMatched.Add(float64(result.MatchedRows))
			}
		}

		finishSpan(span, outcome, err != nil)
	}()

	if err := ctx.Err(); err != nil {
		return result, err
	}

	if plan == nil || plan.plan == nil || plan.group != s.config.Identity.GroupID {
		return result, ErrSQLPlan
	}

	kind = string(plan.Kind())
	if plan.Kind() == sql.Select || plan.Kind() == sql.Explain {
		access = plan.plan.AccessPath()
		aggregate = plan.plan.Aggregate()

		value, err := s.linearRead(ctx, func() (any, error) { return plan.plan.Read(s.db) })
		if err != nil {
			return result, err
		}

		return value.(sql.Result), nil
	}

	command, err := plan.Command()
	if err != nil {
		return result, err
	}

	applied, err := s.Propose(ctx, id, command)
	if err != nil {
		return result, err
	}

	return plan.plan.ApplyResult(applied.Command)
}
