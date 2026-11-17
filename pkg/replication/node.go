package replication

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"sync"
	"uuid"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/raftstore"
)

type Storage interface {
	raft.Storage

	Identity() raftstore.Identity
	Save(raft.Ready) error
}

type StateMachine interface {
	AppliedPosition() (database.LogPosition, error)
	ApplyLogEntry(database.LogEntry) (database.CommandResult, error)
}

type Config struct {
	Store          Storage
	Database       StateMachine
	BootstrapPeers []uint64
}

type Message struct {
	GroupID uuid.UUID
	Raft    *raftpb.Message
}

type Result struct {
	Index     uint64
	Term      uint64
	RequestID uuid.UUID
	Digest    [sha256.Size]byte
	Command   database.CommandResult
}

type Output struct {
	Messages []Message
	Results  []Result
	Reads    []ReadResult
}

type ReadResult struct {
	ID    uint64
	Index uint64
}

type Status struct {
	NodeID   uint64
	LeaderID uint64
	Term     uint64
	Commit   uint64
	Applied  uint64
	IsLeader bool
}

type Node struct {
	mu       sync.Mutex
	raft     *raft.RawNode
	store    Storage
	database StateMachine
	identity raftstore.Identity
	failed   error
	closed   bool
}

func New(config Config) (*Node, Output, error) {
	if config.Store == nil || config.Database == nil {
		return nil, Output{}, ErrInvalidConfig
	}

	identity := config.Store.Identity()
	if identity.GroupID == uuid.Nil() || !validNodeID(identity.NodeID) {
		return nil, Output{}, ErrInvalidConfig
	}

	position, err := config.Database.AppliedPosition()
	if err != nil {
		return nil, Output{}, err
	}

	hard, _, err := config.Store.InitialState()
	if err != nil {
		return nil, Output{}, err
	}

	last, err := config.Store.LastIndex()
	if err != nil {
		return nil, Output{}, err
	}

	first, err := config.Store.FirstIndex()
	if err != nil {
		return nil, Output{}, err
	}

	if first != 1 || position.Index > hard.GetCommit() || hard.GetCommit() > last {
		return nil, Output{}, ErrStateMismatch
	}

	if position.Index > 0 {
		term, err := config.Store.Term(position.Index)
		if err != nil {
			return nil, Output{}, err
		}

		if position.GroupID != identity.GroupID || position.Term != term {
			return nil, Output{}, ErrStateMismatch
		}
	}

	peers, err := bootstrapPeers(config.BootstrapPeers, identity.NodeID)
	if err != nil {
		return nil, Output{}, err
	}

	if len(peers) != 0 && (last != 0 || !raft.IsEmptyHardState(hard) || position.Index != 0) {
		return nil, Output{}, fmt.Errorf("%w: cannot bootstrap an existing member", ErrInvalidConfig)
	}

	raw, err := raft.NewRawNode(&raft.Config{
		ID: identity.NodeID, Storage: config.Store,
		ElectionTick: 10, HeartbeatTick: 1,
		MaxSizePerMsg:             MaxProposalBytes + 1024,
		MaxCommittedSizePerReady:  MaxProposalBytes + 1024,
		MaxUncommittedEntriesSize: 4 * MaxProposalBytes,
		MaxInflightMsgs:           64, CheckQuorum: true, PreVote: true,
		DisableProposalForwarding: true,
		ReadOnlyOption:            raft.ReadOnlySafe,
	})
	if err != nil {
		return nil, Output{}, err
	}

	if len(peers) != 0 {
		if err := raw.Bootstrap(peers); err != nil {
			return nil, Output{}, err
		}
	}

	node := &Node{raft: raw, store: config.Store, database: config.Database, identity: identity}

	output, err := node.drain(false)
	if err != nil {
		return nil, Output{}, err
	}

	return node, output, nil
}

func (n *Node) Tick() (Output, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return Output{}, err
	}

	n.raft.Tick()
	return n.drain(true)
}

func (n *Node) Campaign() (Output, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return Output{}, err
	}

	if err := n.raft.Campaign(); err != nil {
		return Output{}, err
	}

	return n.drain(true)
}

func (n *Node) Propose(requestID uuid.UUID, command []byte) (Output, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return Output{}, err
	}

	if n.raft.BasicStatus().RaftState != raft.StateLeader {
		return Output{}, ErrNotLeader
	}

	encoded, err := EncodeProposal(requestID, command)
	if err != nil {
		return Output{}, err
	}

	if err := n.raft.Propose(encoded); err != nil {
		return Output{}, err
	}

	return n.drain(true)
}

func (n *Node) Step(message Message) (Output, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return Output{}, err
	}

	if err := n.validateMessage(message); err != nil {
		return Output{}, err
	}

	if err := n.raft.Step(proto.Clone(message.Raft).(*raftpb.Message)); err != nil {
		return Output{}, err
	}

	return n.drain(true)
}

func (n *Node) ReadIndex(id uint64) (Output, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return Output{}, err
	}

	if id == 0 {
		return Output{}, ErrInvalidRead
	}

	if n.raft.BasicStatus().RaftState != raft.StateLeader {
		return Output{}, ErrNotLeader
	}

	n.raft.ReadIndex(binary.BigEndian.AppendUint64(nil, id))
	return n.drain(true)
}

func (n *Node) Status() (Status, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return Status{}, err
	}

	state := n.raft.BasicStatus()
	return Status{NodeID: state.ID, LeaderID: state.Lead, Term: state.GetTerm(), Commit: state.GetCommit(), Applied: state.Applied, IsLeader: state.RaftState == raft.StateLeader}, nil
}

func (n *Node) Members() ([]uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return nil, err
	}

	var members []uint64

	for id := range n.raft.Status().Config.Voters.IDs() {
		members = append(members, id)
	}

	slices.Sort(members)
	return members, nil
}

func (n *Node) ReportUnreachable(id uint64) (Output, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.check(); err != nil {
		return Output{}, err
	}

	if !validNodeID(id) || id == n.identity.NodeID {
		return Output{}, ErrInvalidMessage
	}

	n.raft.ReportUnreachable(id)
	return n.drain(true)
}

func (n *Node) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closed = true
}

func (n *Node) check() error {
	if n.closed {
		return ErrClosed
	}

	return n.failed
}

func (n *Node) stop(err error) (Output, error) {
	n.failed = fmt.Errorf("%w: %w", ErrStopped, err)
	return Output{}, n.failed
}

func (n *Node) drain(emitResults bool) (Output, error) {
	var output Output

	for n.raft.HasReady() {
		ready := n.raft.Ready()
		if err := n.store.Save(ready); err != nil {
			return n.stop(err)
		}

		for _, entry := range ready.CommittedEntries {
			result, err := n.apply(entry)
			if err != nil {
				return n.stop(err)
			}

			if emitResults && result.RequestID != uuid.Nil() {
				output.Results = append(output.Results, result)
			}
		}

		for _, message := range ready.Messages {
			output.Messages = append(output.Messages, Message{GroupID: n.identity.GroupID, Raft: proto.Clone(message).(*raftpb.Message)})
		}

		for _, state := range ready.ReadStates {
			if len(state.RequestCtx) != 8 {
				return n.stop(ErrInvalidRead)
			}

			output.Reads = append(output.Reads, ReadResult{ID: binary.BigEndian.Uint64(state.RequestCtx), Index: state.Index})
		}

		n.raft.Advance(ready)
	}

	return output, nil
}

func (n *Node) apply(entry *raftpb.Entry) (Result, error) {
	logged, change, err := decodeEntry(entry)
	if err != nil {
		return Result{}, err
	}

	logged.GroupID = n.identity.GroupID

	result, err := n.database.ApplyLogEntry(logged)
	if err != nil {
		return Result{}, err
	}

	if change != nil {
		n.raft.ApplyConfChange(change)
	}

	return Result{Index: logged.Index, Term: logged.Term, RequestID: logged.RequestID, Digest: sha256.Sum256(logged.Command), Command: result}, nil
}

func bootstrapPeers(ids []uint64, nodeID uint64) ([]raft.Peer, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	if len(ids) > 64 {
		return nil, ErrInvalidConfig
	}

	ordered := slices.Clone(ids)
	slices.Sort(ordered)

	if !slices.Contains(ordered, nodeID) {
		return nil, ErrInvalidConfig
	}

	peers := make([]raft.Peer, len(ordered))
	for i, id := range ordered {
		if !validNodeID(id) || (i > 0 && ordered[i-1] == id) {
			return nil, ErrInvalidConfig
		}

		peers[i] = raft.Peer{ID: id}
	}

	return peers, nil
}

func validNodeID(id uint64) bool {
	return id != 0 && !raft.IsLocalMsgTarget(id)
}
