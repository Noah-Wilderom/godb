package mysqlwire

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func executePacket(id uint32, bitmap, fresh byte, types, values []byte) []byte {
	data := binary.LittleEndian.AppendUint32([]byte{0x17}, id)
	data = append(data, 0, 1, 0, 0, 0, bitmap, fresh)
	data = append(data, types...)
	return append(data, values...)
}

func TestExecuteTypesNullsAndExactFraming(t *testing.T) {
	statement := &preparedStatement{description: sql.Description{Parameters: make([]schema.Column, 3)}}
	values := binary.LittleEndian.AppendUint64(nil, uint64(1)<<63)
	values = binary.LittleEndian.AppendUint64(values, math.MaxUint64)
	values = appendString(values, "quote'\\\x00?")
	packet := executePacket(1, 0, 1, []byte{8, 0, 8, 128, 253, 0}, values)
	args, types, err := decodeExecute(packet, statement)
	require.NoError(t, err)
	require.Equal(t, record.NewInt64(math.MinInt64), args[0])
	require.Equal(t, record.NewUint64(math.MaxUint64), args[1])
	str, err := args[2].Text()
	require.NoError(t, err)
	require.Equal(t, "quote'\\\x00?", str)
	statement.types = types
	reuse := executePacket(1, 7, 0, nil, nil)
	args, _, err = decodeExecute(reuse, statement)
	require.NoError(t, err)
	require.Equal(t, []record.Value{record.NewNull(), record.NewNull(), record.NewNull()}, args)

	for i := range len(packet) {
		_, _, err := decodeExecute(packet[:i], statement)
		require.Error(t, err)
	}

	_, _, err = decodeExecute(append(packet, 1), statement)
	require.Error(t, err)

	for _, offset := range []int{5, 6, 11, 13} {
		bad := append([]byte(nil), packet...)
		bad[offset] = 2
		_, _, err = decodeExecute(bad, statement)
		require.Error(t, err)
	}

	statement.types = nil
	_, _, err = decodeExecute(reuse, statement)
	require.Error(t, err)
}

func TestIntegerParameterWidths(t *testing.T) {
	for _, entry := range []struct {
		kind byte
		size int
	}{{1, 1}, {2, 2}, {3, 4}, {9, 4}, {8, 8}} {
		data := make([]byte, entry.size)
		for i := range data {
			data[i] = 255
		}

		signed, rest, err := decodeParameter(data, parameterType{entry.kind, 0})
		require.NoError(t, err)
		require.Empty(t, rest)
		require.Equal(t, record.NewInt64(-1), signed)
		unsigned, _, err := decodeParameter(data, parameterType{entry.kind, 128})
		require.NoError(t, err)
		n, err := unsigned.Uint64()
		require.NoError(t, err)
		require.Equal(t, uint64(math.MaxUint64)>>uint((8-entry.size)*8), n)
	}
}

func newTestSession(t testing.TB) (*statementSession, *packets, *memoryConn, *int) {
	t.Helper()
	calls := new(int)
	endpoint := &Endpoint{config: Config{Database: "godb", QueryTimeout: time.Second}, query: Handler{
		Describe: func(context.Context, string) (sql.Description, error) {
			return sql.Description{Parameters: []schema.Column{{Type: record.TypeText}}}, nil
		},
		Query: func(_ context.Context, _ string, args ...record.Value) (sql.Result, error) {
			*calls++
			return sql.Result{AffectedRows: 1}, nil
		},
	}}
	conn := &memoryConn{}
	return newStatementSession(endpoint), &packets{conn: conn, sequence: 1}, conn, calls
}

func stmtCommand(kind byte, id uint32) []byte {
	return binary.LittleEndian.AppendUint32([]byte{kind}, id)
}

func longCommand(id uint32, parameter uint16, data []byte) []byte {
	return append(binary.LittleEndian.AppendUint16(stmtCommand(0x18, id), parameter), data...)
}

func responseCode(t testing.TB, conn *memoryConn) uint16 {
	t.Helper()
	data := conn.Bytes()
	require.GreaterOrEqual(t, len(data), 7)
	require.Equal(t, byte(0xff), data[4])
	return binary.LittleEndian.Uint16(data[5:7])
}

func TestLongDataResetCloseAndIsolation(t *testing.T) {
	s, p, conn, calls := newTestSession(t)
	require.NoError(t, s.handle(context.Background(), p, append([]byte{0x16}, "INSERT"...)))
	require.Equal(t, int64(1), s.endpoint.prepared.Load())
	conn.Reset()
	require.NoError(t, s.handle(context.Background(), p, longCommand(1, 0, []byte("first"))))
	require.NoError(t, s.handle(context.Background(), p, longCommand(1, 0, []byte("second"))))
	require.Empty(t, conn.Bytes())
	require.Equal(t, int64(11), s.endpoint.longDataBytes.Load())
	s.endpoint.query.Query = func(_ context.Context, _ string, args ...record.Value) (sql.Result, error) {
		text, err := args[0].Text()
		require.NoError(t, err)
		require.Equal(t, "firstsecond", text)
		*calls++
		return sql.Result{AffectedRows: 1}, nil
	}
	require.NoError(t, s.handle(context.Background(), p, executePacket(1, 0, 1, []byte{253, 0}, nil)))
	require.Equal(t, 1, *calls)
	require.Zero(t, s.endpoint.longDataBytes.Load())
	require.NoError(t, s.handle(context.Background(), p, longCommand(1, 0, []byte("discard"))))
	require.NoError(t, s.handle(context.Background(), p, stmtCommand(0x1a, 1)))
	require.Zero(t, s.endpoint.longDataBytes.Load())
	require.Len(t, s.statements[1].types, 1)
	s.endpoint.query.Query = func(_ context.Context, _ string, args ...record.Value) (sql.Result, error) {
		text, err := args[0].Text()
		require.NoError(t, err)
		require.Equal(t, "new", text)
		*calls++
		return sql.Result{}, nil
	}
	require.NoError(t, s.handle(context.Background(), p, executePacket(1, 0, 0, nil, appendString(nil, "new"))))
	require.Equal(t, 2, *calls)
	conn.Reset()
	require.NoError(t, s.handle(context.Background(), p, stmtCommand(0x19, 1)))
	require.Empty(t, conn.Bytes())
	require.Zero(t, s.endpoint.prepared.Load())
	require.Zero(t, s.endpoint.statementBytes.Load())
	require.NoError(t, s.handle(context.Background(), p, executePacket(1, 0, 0, nil, nil)))
	require.Equal(t, uint16(1243), responseCode(t, conn))
	other, otherPackets, otherConn, otherCalls := newTestSession(t)
	require.NoError(t, other.handle(context.Background(), otherPackets, executePacket(1, 0, 0, nil, nil)))
	require.Equal(t, uint16(1243), responseCode(t, otherConn))
	require.Zero(t, *otherCalls)
}

func TestLongDataLimitsDoNotExecutePartialValues(t *testing.T) {
	s, p, conn, calls := newTestSession(t)
	for range 2 {
		require.NoError(t, s.prepare(context.Background(), p, "INSERT"))
	}

	require.NoError(t, s.appendLong(longCommand(1, 0, make([]byte, maxLongDataBytes))))
	require.NoError(t, s.appendLong(longCommand(2, 0, []byte("overflow"))))
	require.NotNil(t, s.statements[2].pendingError)
	require.Equal(t, maxLongDataBytes, s.longBytes)
	conn.Reset()
	require.NoError(t, s.handle(context.Background(), p, executePacket(2, 0, 1, []byte{253, 0}, nil)))
	require.Equal(t, uint16(1210), responseCode(t, conn))
	require.Zero(t, *calls)
	require.NoError(t, s.appendLong(longCommand(1, 1, []byte("invalid index"))))
	require.Zero(t, s.longBytes)
	require.NotNil(t, s.statements[1].pendingError)
	require.NoError(t, s.handle(context.Background(), p, stmtCommand(0x1a, 1)))
	require.Nil(t, s.statements[1].pendingError)
	require.NoError(t, s.appendLong(longCommand(1, 0, []byte("not retained"))))
	conn.Reset()
	require.NoError(t, s.handle(context.Background(), p, executePacket(1, 0, 1, []byte{8, 0}, nil)))
	require.Equal(t, uint16(1210), responseCode(t, conn))
	require.Zero(t, *calls)
	require.Zero(t, s.longBytes)
	s.close()
	require.Zero(t, s.endpoint.prepared.Load())
	require.Zero(t, s.endpoint.longDataBytes.Load())
	require.Zero(t, s.endpoint.statementBytes.Load())
}

func TestPreparedSQLByteLimit(t *testing.T) {
	s, p, conn, _ := newTestSession(t)

	query := strings.Repeat("x", sql.MaxStatementBytes)
	for range maxStatementBytes / sql.MaxStatementBytes {
		require.NoError(t, s.prepare(context.Background(), p, query))
		conn.Reset()
	}

	require.NoError(t, s.prepare(context.Background(), p, "extra"))
	require.Equal(t, uint16(1461), responseCode(t, conn))
	require.Equal(t, int64(maxStatementBytes), s.endpoint.statementBytes.Load())
	s.close()
	require.Zero(t, s.endpoint.statementBytes.Load())
}

func TestBinaryResultBitmapAcrossBytes(t *testing.T) {
	result := sql.Result{}

	row := make([]record.Value, 10)
	for i := range row {
		result.Columns = append(result.Columns, schema.Column{Name: "col", Type: record.TypeInt64, Nullable: true})
		row[i] = record.NewNull()
	}

	row[0] = record.NewInt64(-1)
	row[9] = record.NewInt64(2)
	result.Rows = [][]record.Value{row}
	output, err := binaryResultPackets("godb", result)
	require.NoError(t, err)
	payload := output[12]
	require.Equal(t, []byte{0, 248, 7}, payload[:3])
	require.Equal(t, uint64(math.MaxUint64), binary.LittleEndian.Uint64(payload[3:11]))
	require.Equal(t, uint64(2), binary.LittleEndian.Uint64(payload[11:]))
}

func FuzzExecuteParameters(f *testing.F) {
	f.Add(executePacket(1, 0, 1, []byte{8, 0}, binary.LittleEndian.AppendUint64(nil, 42)), byte(1))
	f.Add(executePacket(1, 1, 0, nil, nil), byte(1))
	f.Fuzz(func(t *testing.T, data []byte, count byte) {
		if len(data) > maxPacket {
			return
		}

		n := int(count % 65)

		statement := &preparedStatement{description: sql.Description{Parameters: make([]schema.Column, n)}, types: make([]parameterType, n)}
		for i := range statement.types {
			statement.types[i] = parameterType{253, 0}
		}

		args, types, err := decodeExecute(data, statement)
		if err == nil {
			require.Len(t, args, n)
			require.Len(t, types, n)
		}
	})
}

func BenchmarkDecodeExecute(b *testing.B) {
	statement := &preparedStatement{description: sql.Description{Parameters: make([]schema.Column, 2)}}
	payload := binary.LittleEndian.AppendUint64(nil, 42)
	payload = appendString(payload, "parameter")
	packet := executePacket(1, 0, 1, []byte{8, 0, 253, 0}, payload)
	b.ReportAllocs()

	for b.Loop() {
		if _, _, err := decodeExecute(packet, statement); err != nil {
			b.Fatal(err)
		}
	}
}
