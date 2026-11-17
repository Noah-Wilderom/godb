package server

import (
	"bytes"
	"context"
	"errors"
	"time"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	peerv1 "github.com/Noah-Wilderom/godb/api/peer/v1"
	"github.com/Noah-Wilderom/godb/internal/observe"
	"github.com/Noah-Wilderom/godb/pkg/raftstore"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

type peerService struct {
	peerv1.UnimplementedPeerServer

	server *Server
	slots  chan struct{}
}

func (p *peerService) Step(ctx context.Context, request *peerv1.StepRequest) (_ *emptypb.Empty, err error) {
	s := p.server
	start := time.Now()
	defer func() {
		observe.Since(s.metrics.peerReceive, start)

		if err != nil {
			s.metrics.peerRejected.Inc()
		}
	}()
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "peer queue is full")
	}

	if request == nil || !bytes.Equal(request.GroupId, s.config.Identity.GroupID[:]) || len(request.Message) == 0 || len(request.Message) > replication.MaxMessageBytes {
		return nil, status.Error(codes.InvalidArgument, "invalid peer envelope")
	}

	message := &raftpb.Message{}
	if err := (proto.UnmarshalOptions{RecursionLimit: 32}).Unmarshal(request.Message, message); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid Raft message")
	}

	remote, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "peer certificate required")
	}

	tlsInfo, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 || !matchesIdentity(tlsInfo.State.PeerCertificates[0], s.config.Identity.GroupID, message.GetFrom()) {
		return nil, status.Error(codes.PermissionDenied, "certificate does not match sender")
	}

	if _, ok := s.config.Peers[message.GetFrom()]; !ok {
		return nil, status.Error(codes.PermissionDenied, "unknown member")
	}

	work := inbound{ctx: ctx, message: replication.Message{GroupID: s.config.Identity.GroupID, Raft: message}, reply: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-s.ctx.Done():
		return nil, status.Error(codes.Unavailable, "member stopped")
	case s.inbound <- work:
	}

	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-s.ctx.Done():
		return nil, status.Error(codes.Unavailable, "member stopped")
	case err := <-work.reply:
		if err != nil {
			return nil, peerError(err)
		}

		return &emptypb.Empty{}, nil
	}
}

func peerError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}

	if errors.Is(err, replication.ErrInvalidMessage) || errors.Is(err, raft.ErrStepPeerNotFound) || errors.Is(err, raftstore.ErrSnapshotUnsupported) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	return status.Error(codes.Unavailable, "member could not process message")
}
