package server

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"time"

	"go.etcd.io/raft/v3"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Noah-Wilderom/godb/pkg/raftstore"
)

var (
	ErrInvalidConfig  = errors.New("invalid server configuration")
	ErrClosed         = errors.New("server is closed")
	ErrBusy           = errors.New("server request capacity reached")
	ErrRequestPending = errors.New("request ID already has a pending proposal")
)

type Config struct {
	MySQL          MySQLConfig
	MetricsAddress string
	TracerProvider trace.TracerProvider
	Identity       raftstore.Identity
	DatabasePath   string
	RaftPath       string
	Peers          map[uint64]string
	Bootstrap      bool
	CertFile       string
	KeyFile        string
	CAFile         string
	TickInterval   time.Duration
	RPCTimeout     time.Duration
}

func (c Config) normalized() (Config, error) {
	if c.TracerProvider == nil {
		c.TracerProvider = noop.NewTracerProvider()
	}

	if c.DatabasePath == "" || c.RaftPath == "" || c.CertFile == "" || c.KeyFile == "" || c.CAFile == "" || c.Identity.NodeID == 0 || raft.IsLocalMsgTarget(c.Identity.NodeID) || c.Identity.GroupID == ([16]byte{}) || len(c.Peers) == 0 || len(c.Peers) > 64 {
		return Config{}, ErrInvalidConfig
	}

	if _, ok := c.Peers[c.Identity.NodeID]; !ok {
		return Config{}, fmt.Errorf("%w: local member is missing from peers", ErrInvalidConfig)
	}

	c.Peers = maps.Clone(c.Peers)
	addresses := make(map[string]bool)

	for id, address := range c.Peers {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return Config{}, fmt.Errorf("%w: peer %d address: %w", ErrInvalidConfig, id, err)
		}

		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 || host == "" || id == 0 || raft.IsLocalMsgTarget(id) || addresses[address] {
			return Config{}, fmt.Errorf("%w: invalid or repeated peer endpoint", ErrInvalidConfig)
		}

		addresses[address] = true
	}

	if c.TickInterval == 0 {
		c.TickInterval = 100 * time.Millisecond
	}

	if c.RPCTimeout == 0 {
		c.RPCTimeout = time.Second
	}

	if c.TickInterval < time.Millisecond || c.RPCTimeout <= 0 {
		return Config{}, ErrInvalidConfig
	}

	return c, nil
}

func (c Config) memberIDs() []uint64 {
	ids := make([]uint64, 0, len(c.Peers))
	for id := range c.Peers {
		ids = append(ids, id)
	}

	slices.Sort(ids)
	return ids
}
