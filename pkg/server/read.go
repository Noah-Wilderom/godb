package server

import (
	"context"
	"math"
	"time"

	"github.com/Noah-Wilderom/godb/pkg/replication"
)

type readRequest struct {
	ctx         context.Context
	run         func() (any, error)
	reply       chan readResponse
	term, index uint64
}

type readResponse struct {
	value any
	err   error
}

func (s *Server) linearRead(ctx context.Context, run func() (any, error)) (value any, err error) {
	start := time.Now()
	ctx, span := s.tracer.Start(ctx, "godb.read")
	defer func() {
		outcome := proposalOutcome(replication.Result{}, err)
		if err == nil {
			outcome = "ok"
		}

		s.metrics.reads.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
		finishSpan(span, outcome, err != nil)
	}()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	request := &readRequest{ctx: ctx, run: run, reply: make(chan readResponse, 1)}
	select {
	case <-s.ctx.Done():
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case s.reads <- request:
	default:
		return nil, ErrBusy
	}

	select {
	case response := <-request.reply:
		return response.value, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, ErrClosed
	}
}

func (s *Server) beginRead(request *readRequest, pending map[uint64]*readRequest) (output replication.Output, err error) {
	defer func() {
		if err != nil {
			request.reply <- readResponse{err: err}
		}
	}()

	if err := request.ctx.Err(); err != nil {
		return output, err
	}

	state, err := s.node.Status()
	if err != nil {
		return output, err
	}

	if !state.IsLeader {
		return output, replication.ErrNotLeader
	}

	if len(pending) >= maxPending || s.readSequence == math.MaxUint64 {
		return output, ErrBusy
	}

	s.readSequence++
	request.term = state.Term
	pending[s.readSequence] = request

	output, err = s.node.ReadIndex(s.readSequence)
	if err != nil {
		delete(pending, s.readSequence)
	}

	return output, err
}

func (s *Server) completeReads(results []replication.ReadResult, state replication.Status, pending map[uint64]*readRequest) {
	for _, result := range results {
		if request := pending[result.ID]; request != nil {
			request.index = result.Index
		}
	}

	for id, request := range pending {
		if !state.IsLeader || state.Term != request.term {
			request.reply <- readResponse{err: replication.ErrNotLeader}
			delete(pending, id)
			continue
		}

		if request.index == 0 || state.Applied < request.index {
			if request.ctx.Err() != nil {
				request.run = nil
			}

			continue
		}

		delete(pending, id)

		if err := request.ctx.Err(); err != nil {
			request.reply <- readResponse{err: err}
			continue
		}

		value, err := request.run()
		request.reply <- readResponse{value: value, err: err}
	}

	s.metrics.readPending.Set(float64(len(pending)))
}
