package mysqlwire

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Noah-Wilderom/godb/internal/observe"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

type Config struct {
	TLS                                         *tls.Config
	User, Database                              string
	PasswordHash                                [sha256.Size]byte
	MaxConnections                              int
	HandshakeTimeout, IdleTimeout, QueryTimeout time.Duration
}

type Handler struct {
	Catalog  func(context.Context, string) ([]schema.Table, error)
	Query    func(context.Context, string, ...record.Value) (sql.Result, error)
	Describe func(context.Context, string) (sql.Description, error)
}

type Endpoint struct {
	listener                                              net.Listener
	config                                                Config
	query                                                 Handler
	mu                                                    sync.Mutex
	clients                                               map[net.Conn]struct{}
	closed                                                bool
	workers                                               sync.WaitGroup
	sequence                                              atomic.Uint32
	sessionCommands, sessionErrors, resets                atomic.Uint64
	discoveryCommands, discoveryErrors                    atomic.Uint64
	informationCommands, informationErrors                atomic.Uint64
	prepared, statementBytes, longDataBytes               atomic.Int64
	prepares, executes, statementErrors                   atomic.Uint64
	open, peak, sessions                                  atomic.Int64
	opened, ended, authFailures, rejected, protocolErrors atomic.Uint64
}

func New(listener net.Listener, config Config, query Handler) (*Endpoint, error) {
	if listener == nil || query.Query == nil || config.TLS == nil || len(config.TLS.Certificates) == 0 || config.User == "" || len(config.User) > 128 || config.Database == "" || len(config.Database) > 64 || config.PasswordHash == ([sha256.Size]byte{}) {
		return nil, errors.New("invalid MySQL endpoint configuration")
	}

	if config.MaxConnections == 0 {
		config.MaxConnections = 128
	}

	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = 10 * time.Second
	}

	if config.IdleTimeout == 0 {
		config.IdleTimeout = time.Minute
	}

	if config.QueryTimeout == 0 {
		config.QueryTimeout = 5 * time.Second
	}

	if config.MaxConnections < 1 || config.MaxConnections > 4096 || config.HandshakeTimeout <= 0 || config.IdleTimeout <= 0 || config.QueryTimeout <= 0 {
		return nil, errors.New("invalid MySQL endpoint limits")
	}

	config.TLS = config.TLS.Clone()
	if config.TLS.MinVersion < tls.VersionTLS13 {
		config.TLS.MinVersion = tls.VersionTLS13
	}

	return &Endpoint{listener: listener, config: config, query: query, clients: make(map[net.Conn]struct{})}, nil
}

func (e *Endpoint) Address() string { return e.listener.Addr().String() }

func (e *Endpoint) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true

	_ = e.listener.Close()
	for client := range e.clients {
		_ = client.Close()
	}
}

func (e *Endpoint) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, e.Close)
	defer stop()
	defer e.workers.Wait()
	defer e.Close()

	for {
		conn, err := e.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}

			return err
		}

		e.mu.Lock()
		if e.closed || len(e.clients) >= e.config.MaxConnections {
			e.mu.Unlock()
			e.rejected.Add(1)
			_ = conn.Close()
			continue
		}

		e.clients[conn] = struct{}{}
		e.mu.Unlock()
		current := e.open.Add(1)
		e.opened.Add(1)

		for previous := e.peak.Load(); current > previous; previous = e.peak.Load() {
			if e.peak.CompareAndSwap(previous, current) {
				break
			}
		}

		e.workers.Go(func() {
			defer func() {
				_ = conn.Close()
				e.mu.Lock()
				delete(e.clients, conn)
				e.mu.Unlock()
				e.open.Add(-1)
				e.ended.Add(1)
			}()
			e.serveClient(ctx, conn)
		})
	}
}

func (e *Endpoint) serveClient(ctx context.Context, conn net.Conn) {
	packets := &packets{conn: conn}

	id := e.sequence.Add(1)
	if err := e.authenticate(ctx, packets, id); err != nil {
		e.authFailures.Add(1)
		return
	}

	session := newStatementSession(e)
	session.state.id = id
	defer session.close()
	e.sessions.Add(1)
	defer e.sessions.Add(-1)

	for {
		packets.sequence = 0
		if err := packets.conn.SetDeadline(time.Now().Add(e.config.IdleTimeout)); err != nil {
			return
		}

		command, err := packets.read()
		if err != nil {
			if errors.Is(err, errProtocol) {
				e.protocolErrors.Add(1)
			}

			return
		}

		if command[0] == 1 {
			return
		}

		if err := packets.conn.SetDeadline(time.Now().Add(e.config.QueryTimeout)); err != nil {
			return
		}

		switch command[0] {
		case 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1f:
			err = session.handle(ctx, packets, command)
		case 14:
			if len(command) != 1 {
				err = packets.writeError(errProtocol)
			} else {
				err = packets.ok(0)
			}
		case 2:
			if string(command[1:]) != e.config.Database {
				err = packets.writeError(&Error{1049, "42000", "Unknown database"})
			} else {
				err = packets.ok(0)
			}
		case 3:
			requestCtx, cancel := context.WithTimeout(ctx, e.config.QueryTimeout)
			result, queryErr := session.query(requestCtx, string(command[1:]))
			cancel()

			if err := packets.conn.SetWriteDeadline(time.Now().Add(e.config.QueryTimeout)); err != nil {
				return
			}

			if queryErr != nil {
				err = packets.writeError(queryErr)
			} else if len(result.Columns) == 0 {
				err = packets.resultOK(result)
			} else {
				var output [][]byte

				output, err = resultPackets(e.config.Database, result)
				if err != nil {
					err = packets.writeError(err)
				} else {
					for _, payload := range output {
						if err = packets.write(payload); err != nil {
							break
						}
					}
				}
			}
		default:
			err = packets.writeError(&Error{1235, "42000", "MySQL command is not implemented by GoDB"})
		}

		if err != nil {
			if errors.Is(err, errProtocol) {
				e.protocolErrors.Add(1)
			}

			return
		}
	}
}

func (e *Endpoint) Metrics() prometheus.Collector {
	return observe.Group{
		observe.Counter("mysql_information_schema_commands", "INFORMATION_SCHEMA execution attempts, excluding prepare descriptions.", func() float64 { return float64(e.informationCommands.Load()) }),
		observe.Counter("mysql_information_schema_errors", "INFORMATION_SCHEMA execution errors before result encoding.", func() float64 { return float64(e.informationErrors.Load()) }),
		observe.Counter("mysql_discovery_commands", "Schema discovery execution attempts, excluding prepare descriptions.", func() float64 { return float64(e.discoveryCommands.Load()) }),
		observe.Counter("mysql_discovery_errors", "Schema discovery execution errors before result encoding.", func() float64 { return float64(e.discoveryErrors.Load()) }),
		observe.Counter("mysql_session_commands", "Local session SQL executions, including errors; excludes prepare descriptions.", func() float64 { return float64(e.sessionCommands.Load()) }),
		observe.Counter("mysql_session_errors", "Local session SQL execution errors.", func() float64 { return float64(e.sessionErrors.Load()) }),
		observe.Counter("mysql_connection_resets", "Successful COM_RESET_CONNECTION commands.", func() float64 { return float64(e.resets.Load()) }),
		observe.Gauge("mysql_prepared_statements", "Open connection-scoped prepared statement handles.", func() float64 { return float64(e.prepared.Load()) }),
		observe.Gauge("mysql_prepared_statement_bytes", "SQL text bytes held by prepared statements.", func() float64 { return float64(e.statementBytes.Load()) }),
		observe.Gauge("mysql_long_data_bytes", "Buffered prepared parameter bytes awaiting execution or reset.", func() float64 { return float64(e.longDataBytes.Load()) }),
		observe.Counter("mysql_stmt_prepare", "COM_STMT_PREPARE calls, including failures.", func() float64 { return float64(e.prepares.Load()) }),
		observe.Counter("mysql_stmt_execute", "COM_STMT_EXECUTE calls for known statement handles, including failures.", func() float64 { return float64(e.executes.Load()) }),
		observe.Counter("mysql_statement_errors", "Prepared statement error responses, including binding and execution failures.", func() float64 { return float64(e.statementErrors.Load()) }),
		observe.Gauge("mysql_connections", "Accepted MySQL TCP connections, including TLS/authentication and idle clients.", func() float64 { return float64(e.open.Load()) }),
		observe.Gauge("mysql_connections_peak", "Peak simultaneous accepted MySQL TCP connections since startup.", func() float64 { return float64(e.peak.Load()) }),
		observe.Gauge("mysql_sessions", "Authenticated MySQL sessions, including idle sessions.", func() float64 { return float64(e.sessions.Load()) }),
		observe.Counter("mysql_connections_opened", "Accepted MySQL TCP connections since startup.", func() float64 { return float64(e.opened.Load()) }),
		observe.Counter("mysql_connections_closed", "Accepted MySQL TCP connections closed since startup.", func() float64 { return float64(e.ended.Load()) }),
		observe.Counter("mysql_auth_failures", "Failed MySQL handshakes, including TLS, malformed login and credential errors.", func() float64 { return float64(e.authFailures.Load()) }),
		observe.Counter("mysql_connections_rejected", "MySQL TCP connections rejected before handshake due to capacity or shutdown.", func() float64 { return float64(e.rejected.Load()) }),
		observe.Counter("mysql_protocol_errors", "Malformed command packets causing connection closure.", func() float64 { return float64(e.protocolErrors.Load()) }),
	}
}
