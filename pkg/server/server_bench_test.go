package server

import (
	"context"
	"encoding/binary"
	"testing"
	"time"
	"uuid"
)

func BenchmarkServerPeerRPC(b *testing.B) {
	c := setupCluster(b, 2)
	c.start(b, 1)

	tlsConfig, err := loadTLS(c.configs[2])
	if err != nil {
		b.Fatal(err)
	}

	client := rpcClient(b, c.configs[1].Peers[1], tlsConfig.client(1))

	request := heartbeat(b, 2)
	if err := callPeer(client, request); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if err := callPeer(client, request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkServerPropose(b *testing.B) {
	c := setupCluster(b, 1)
	c.start(b, 1)
	member := c.members[1]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		state, err := member.Status()
		if err != nil {
			b.Fatal(err)
		}

		if state.IsLeader {
			break
		}

		select {
		case <-ctx.Done():
			b.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}

	command := putCommand(b, "key", "value")
	id := uuid.UUID{1}

	var index uint64

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		index++
		binary.LittleEndian.PutUint64(id[8:], index)
		requestCtx, stop := context.WithTimeout(context.Background(), time.Second)
		_, err := member.Propose(requestCtx, id, command)
		stop()

		if err != nil {
			b.Fatal(err)
		}
	}
}
