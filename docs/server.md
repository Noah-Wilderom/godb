# Running database members

`godb server` opens an existing database, recovers its Raft journal, runs the
replication loop, and serves the authenticated peer RPC in `api/peer/v1/peer.proto`.
The peer endpoint is separate from the opt-in [MySQL client endpoint](mysql.md),
enabled with `--mysql-listen`, `--mysql-user`, and `--mysql-password-file`.
Embedders can submit commands and execute the [SQL subset](sql.md) through the Go API,
with replicated writes and quorum-confirmed primary-key reads on the leader.

Built-in [metrics and optional OpenTelemetry tracing](observability.md) are available
through `--metrics-listen`, `--otlp-http-endpoint`, and `--trace-sample-ratio`.
See the [production-readiness checklist](production-readiness.md) for remaining gates.

## Starting a group

Each initial member needs its own empty database and journal, a distinct nonzero ID,
and a certificate for that identity. All initial members use the same nonzero group
UUID and the same complete ID-to-address map.

```sh
godb database create ./node1.db
godb server \
  --database ./node1.db \
  --group 11111111-1111-4111-8111-111111111111 \
  --node 1 \
  --peer 1=127.0.0.1:7001 \
  --peer 2=127.0.0.1:7002 \
  --peer 3=127.0.0.1:7003 \
  --bootstrap \
  --tls-cert ./certs/node1.crt \
  --tls-key ./certs/node1.key \
  --tls-ca ./certs/ca.crt
```

Run corresponding commands for nodes 2 and 3 with their database, ID, and certificate
paths, retaining all three peer entries. A single-member group uses only its own peer
entry. The server elects a leader through ordinary timer events.

Bootstrap permits initializing a missing or empty journal and requires an empty
database. The default journal path is the database path plus `.raft`; `--raft`
overrides it. Without bootstrap, a missing or empty journal is an error. An existing
nonempty journal is always recovered, even if bootstrap remains enabled. It is never
reset. Configured member IDs must match durable membership on restart. Do not clone
a journal into another member or delete it to restart a node. An already bound
database cannot be bootstrapped against a missing journal.

`--listen` overrides the bind address; by default it is this member's peer address.
Peer addresses must be reachable from other members and include a nonzero port.
Bind addresses may differ for NAT or containers. The startup line goes to standard
output; the Raft library currently uses its default diagnostic logger. Startup does
not mean that a leader or quorum is already available.

Ctrl-C and SIGTERM stop the server. Shutdown cancels peer RPCs, joins workers, and
closes the database, journal, and listener. Storage work already executing finishes
before cleanup. A storage failure stops participation and makes the CLI exit with an
error. Pending callers may receive an ambiguous error even if their command committed.

## Certificates

Connections require mutual TLS 1.3; there is no plaintext mode. Member certificates
must be valid under the configured CA, support both server/client authentication,
and have a DNS/IP SAN matching their advertised address. They must also have exactly
one URI SAN: `spiffe://godb/<group-uuid>/node/<node-id>`.

The URI is our identity convention, not a SPIFFE workload discovery implementation.
The local certificate is checked before opening files. Outgoing connections verify
the hostname, chain, and expected destination identity. Incoming connections require
a configured member, and each RPC sender ID must match that certificate. Group
routing is checked separately. Certificate rotation currently requires restart.

This OpenSSL recipe creates local test certificates for the example above. Keep the
CA private key out of member containers; each member needs only its own key/cert and
the public CA bundle.

```sh
umask 077
mkdir -p certs
group=11111111-1111-4111-8111-111111111111
openssl genpkey -algorithm ED25519 -out certs/ca.key
openssl req -new -x509 -key certs/ca.key -out certs/ca.crt -days 365 \
  -subj '/CN=godb-local-ca' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign'
for id in 1 2 3; do
  openssl genpkey -algorithm ED25519 -out "certs/node$id.key"
  openssl req -new -key "certs/node$id.key" -out "certs/node$id.csr" \
    -subj "/CN=godb-node-$id"
  cat > "certs/node$id.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=IP:127.0.0.1,URI:spiffe://godb/$group/node/$id
EOF
  openssl x509 -req -in "certs/node$id.csr" -CA certs/ca.crt \
    -CAkey certs/ca.key -CAcreateserial -out "certs/node$id.crt" \
    -days 30 -extfile "certs/node$id.ext"
done
```

Use appropriate host SANs and your managed CA for non-local deployments. The scratch
image uses mounted PEM files; its peer roots come from `--tls-ca`, so an OS CA bundle
is unnecessary. Mount the writable database directory separately from read-only
certificates. The existing `/var/lib/godb` working directory remains valid.

## Processing and limits

One event loop serializes ticks, inbound messages, proposals, read barriers, SQL
planning/point reads, and unreachable-peer notifications. Every remote peer has an asynchronous sender and connection, so a
slow member doesn't block healthy peers or ticks. Messages leave the loop only after
the [replication layer](replication-loop.md) completes required persistence.

The unary Step RPC carries a group UUID and opaque Raft protobuf bytes. Its success
means local processing completed, not that a user command committed on a quorum.
TLS uses gRPC's [transport credentials](https://grpc.io/docs/guides/auth/).

| Limit | Value |
| --- | --- |
| Default tick | 100 ms; configurable with `--tick` |
| Default peer RPC timeout | 1 second; configurable with `--peer-timeout` |
| Outstanding outbound data per peer | 8 MiB, including the in-flight call |
| Queued messages per peer | 32 |
| Inbound handler slots | 16 across the service |
| Raft message payload | 8 MiB |
| Queued local proposals | 32 |
| Pending local proposals | 128 |
| Queued read/catalog requests | 32 |
| Outstanding read barriers | 128, including canceled requests awaiting acknowledgement |

Overflow drops outbound messages without blocking and reports the peer unreachable
to Raft. RPC failures also report unreachable. Raft handles retransmission; transport
doesn't keep another durable queue. TransportStats exposes process-local successful,
failed, and dropped message counts. Queue limits aren't a global memory/connection quota.

## Go lifecycle and proposals

`server.Start(ctx, config, listener)` recovers and starts a member without waiting
for election. On success it owns the listener and both storage handles. On failure,
it closes handles it opened; the caller remains responsible for the supplied listener.
Status returns local Raft status. Wait waits for shutdown and returns its error.
Close requests shutdown and waits; context cancellation also requests shutdown.
Don't access or close owned storage handles independently while the server is running.

`Propose(ctx, requestID, encodedCommand)` waits for that exact command to commit and
apply locally. It can return ErrNotLeader, ErrBusy, ErrRequestPending, a context
error, or a shutdown/processing error. Deterministic command rejection remains a
nil Go error plus a non-`ok` result code. One active caller per request ID is allowed.
Cancelled waiters are removed without removing already proposed Raft entries.

Results match both request ID and SHA-256 command digest. A late result for an old
command cannot acknowledge a different command with that same ID. Reusing an ID with
different bytes produces the database's durable request-ID-conflict outcome.
Leadership loss releases pending waiters with ErrNotLeader, but their entries might
still commit later. Use bounded contexts and retry ambiguous writes with identical
IDs and bytes. Status reporting leadership does not make local reads linearizable.

`PrepareSQL(ctx, query, args...)` obtains a consistent catalog view and an immutable
SQL plan. `ExecuteSQL(ctx, requestID, plan)` submits writes through Propose or reads
after a new ReadIndex/applied-index barrier. Keep the plan and ID for ambiguous
write retries; see [allocation, retry, and cancellation semantics](sql.md#sql-through-a-raft-server).
Followers reject these calls; callers must select the leader.

The MySQL endpoint supports the SQL subset over TLS, with no automatic leader
routing or idempotent wire retries. This step does not add proposal forwarding, dynamic membership,
snapshots, compaction, sharding, or automatic horizontal scaling.

## Bindings and validation

Bindings are checked in, so builds don't need protoc. `task tools:proto` installs
pinned Go generators; `task proto` uses your installed protoc to regenerate bindings
(generated here with protoc 36.1). The workflow follows the official
[Go gRPC tutorial](https://grpc.io/docs/languages/go/basics/).

Release scanning uses `gosec -exclude-generated ./...`. Standard protobuf-generated
`unsafe` descriptor access triggers G103; all handwritten server, TLS, storage, and
CLI code remain scanned. Don't edit generated files to suppress findings.

Tests run independent members over loopback TCP/mTLS and cover replication, retries,
leader/quorum loss, catch-up, restart, rejected certificates, sender spoofing, size
and queue bounds, apply failure recovery, cancellation, file-lock release, and CLI
stdout routing. Benchmarks measure peer RPC round trips and local durable proposals,
not production cluster throughput.
