package mysqlwire

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

const (
	maxStatements     = 128
	maxStatementBytes = 1 << 20
	maxLongDataBytes  = 128 << 10
)

type preparedStatement struct {
	query        string
	description  sql.Description
	types        []parameterType
	longData     map[int][]byte
	longBytes    int
	pendingError error
}

type statementSession struct {
	state            connectionState
	endpoint         *Endpoint
	statements       map[uint32]*preparedStatement
	next             uint32
	bytes, longBytes int
}

func newStatementSession(endpoint *Endpoint) *statementSession {
	return &statementSession{endpoint: endpoint, statements: make(map[uint32]*preparedStatement)}
}

func (s *statementSession) clearLong(statement *preparedStatement) {
	s.longBytes -= statement.longBytes
	s.endpoint.longDataBytes.Add(-int64(statement.longBytes))
	statement.longBytes = 0
	statement.longData = nil
	statement.pendingError = nil
}

func (s *statementSession) remove(id uint32) {
	if statement := s.statements[id]; statement != nil {
		s.clearLong(statement)
		s.bytes -= len(statement.query)
		s.endpoint.statementBytes.Add(-int64(len(statement.query)))
		s.endpoint.prepared.Add(-1)
		delete(s.statements, id)
	}
}

func (s *statementSession) close() {
	for id := range s.statements {
		s.remove(id)
	}
}

func (s *statementSession) fail(p *packets, err error) error {
	s.endpoint.statementErrors.Add(1)

	if errors.Is(err, errProtocol) {
		err = &Error{1210, "HY000", "Malformed prepared statement packet"}
	}

	return p.writeError(err)
}

func (s *statementSession) handle(ctx context.Context, p *packets, command []byte) error {
	switch command[0] {
	case 0x1f:
		if len(command) != 1 {
			return errProtocol
		}

		s.close()
		s.state = connectionState{id: s.state.id}
		s.endpoint.resets.Add(1)
		return p.ok(0)
	case 0x16:
		return s.prepare(ctx, p, string(command[1:]))
	case 0x18:
		return s.appendLong(command)
	}

	if len(command) < 5 {
		return errProtocol
	}

	id := binary.LittleEndian.Uint32(command[1:5])
	if command[0] == 0x19 {
		if len(command) != 5 {
			return errProtocol
		}

		s.remove(id)
		return nil
	}

	statement := s.statements[id]
	if statement == nil {
		return s.fail(p, &Error{1243, "HY000", "Unknown prepared statement"})
	}

	if command[0] == 0x1a {
		if len(command) != 5 {
			return s.fail(p, errProtocol)
		}

		s.clearLong(statement)
		return p.ok(0)
	}

	s.endpoint.executes.Add(1)
	defer s.clearLong(statement)

	if statement.pendingError != nil {
		return s.fail(p, statement.pendingError)
	}

	args, types, err := decodeExecute(command, statement)
	if err != nil {
		return s.fail(p, err)
	}

	statement.types = types
	requestCtx, cancel := context.WithTimeout(ctx, s.endpoint.config.QueryTimeout)
	result, err := s.query(requestCtx, statement.query, args...)
	cancel()

	if deadlineErr := p.conn.SetWriteDeadline(time.Now().Add(s.endpoint.config.QueryTimeout)); deadlineErr != nil {
		return deadlineErr
	}

	if err != nil {
		return s.fail(p, err)
	}

	if !slices.Equal(result.Columns, statement.description.Columns) {
		return s.fail(p, &Error{1615, "HY000", "Prepared statement needs to be prepared again"})
	}

	if len(result.Columns) == 0 {
		return p.resultOK(result)
	}

	output, err := binaryResultPackets(s.endpoint.config.Database, result)
	if err != nil {
		return s.fail(p, err)
	}

	for _, payload := range output {
		if err := p.write(payload); err != nil {
			return err
		}
	}

	return nil
}

func (s *statementSession) prepare(ctx context.Context, p *packets, query string) error {
	s.endpoint.prepares.Add(1)

	if len(query) > sql.MaxStatementBytes || len(s.statements) >= maxStatements || len(query) > maxStatementBytes-s.bytes || s.next == math.MaxUint32 {
		return s.fail(p, &Error{1461, "42000", "Prepared statement resource limit reached"})
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.endpoint.config.QueryTimeout)
	description, err := s.describe(requestCtx, query)
	cancel()

	if deadlineErr := p.conn.SetWriteDeadline(time.Now().Add(s.endpoint.config.QueryTimeout)); deadlineErr != nil {
		return deadlineErr
	}

	if err != nil {
		return s.fail(p, err)
	}

	columnCount, parameterCount := len(description.Columns), len(description.Parameters)
	if columnCount > 64 || parameterCount > 64 {
		return s.fail(p, errProtocol)
	}

	id := s.next + 1
	payload := binary.LittleEndian.AppendUint32([]byte{0}, id)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(columnCount))
	payload = binary.LittleEndian.AppendUint16(payload, uint16(parameterCount))
	payload = append(payload, 0, 0, 0)
	output := [][]byte{payload}

	for _, columns := range [][]schema.Column{description.Parameters, description.Columns} {
		for _, column := range columns {
			encoded, err := columnPacket(s.endpoint.config.Database, column)
			if err != nil {
				return s.fail(p, err)
			}

			output = append(output, encoded)
		}

		if len(columns) > 0 {
			output = append(output, []byte{0xfe, 0, 0, 2, 0})
		}
	}

	description.Columns = slices.Clone(description.Columns)
	description.Parameters = slices.Clone(description.Parameters)
	s.next = id
	s.statements[id] = &preparedStatement{query: query, description: description}
	s.bytes += len(query)
	s.endpoint.statementBytes.Add(int64(len(query)))
	s.endpoint.prepared.Add(1)

	for _, payload := range output {
		if err := p.write(payload); err != nil {
			return err
		}
	}

	return nil
}

func (s *statementSession) appendLong(command []byte) error {
	if len(command) < 7 {
		return errProtocol
	}

	statement := s.statements[binary.LittleEndian.Uint32(command[1:5])]
	if statement == nil || statement.pendingError != nil {
		return nil
	}

	parameter := int(binary.LittleEndian.Uint16(command[5:7]))
	if parameter >= len(statement.description.Parameters) || len(command)-7 > maxLongDataBytes-s.longBytes {
		s.clearLong(statement)
		statement.pendingError = &Error{1210, "HY000", "Invalid or oversized long parameter data"}
		return nil
	}

	if statement.longData == nil {
		statement.longData = make(map[int][]byte)
	}

	statement.longData[parameter] = append(statement.longData[parameter], command[7:]...)
	statement.longBytes += len(command) - 7
	s.longBytes += len(command) - 7
	s.endpoint.longDataBytes.Add(int64(len(command) - 7))
	return nil
}
