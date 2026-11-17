package mysqlwire

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

type memoryConn struct{ bytes.Buffer }

func (*memoryConn) Close() error                     { return nil }
func (*memoryConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*memoryConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*memoryConn) SetDeadline(time.Time) error      { return nil }
func (*memoryConn) SetReadDeadline(time.Time) error  { return nil }
func (*memoryConn) SetWriteDeadline(time.Time) error { return nil }

func TestPacketBoundsAndSequence(t *testing.T) {
	for _, size := range []int{1, 250, 65536, maxPacket} {
		conn := &memoryConn{}
		sender := &packets{conn: conn}
		expected := bytes.Repeat([]byte{8}, size)
		require.NoError(t, sender.write(expected))
		reader := &packets{conn: conn}
		actual, err := reader.read()
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}

	for _, header := range [][]byte{{0, 0, 0, 0}, {1, 0, 0, 1}, {255, 255, 255, 0}, {1, 0, 2, 0}} {
		conn := &memoryConn{}
		_, err := conn.Write(header)
		require.NoError(t, err)
		_, err = (&packets{conn: conn}).read()
		require.ErrorIs(t, err, errProtocol)
	}

	conn := &memoryConn{}
	sender := &packets{conn: conn}
	require.ErrorIs(t, sender.write(make([]byte, maxPacket+1)), errProtocol)
	require.Zero(t, conn.Len())
}

func loginPacket() []byte {
	data := binary.LittleEndian.AppendUint32(nil, capabilities)
	data = append(data, make([]byte, 28)...)
	data[8] = 45
	data = append(data, []byte("user\x00")...)
	data = append(data, 32)
	data = append(data, make([]byte, 32)...)
	data = append(data, []byte("godb\x00")...)
	data = append(data, authPlugin...)
	return append(data, 0)
}

func TestLoginStrictFraming(t *testing.T) {
	data := loginPacket()
	info, err := parseLogin(data, capabilities)
	require.NoError(t, err)
	require.Equal(t, login{"user", "godb", authPlugin}, info)

	for size := range len(data) {
		_, err := parseLogin(data[:size], capabilities)
		require.Error(t, err)
	}

	data[8] = 8
	_, err = parseLogin(data, capabilities)
	require.Error(t, err)
	data = loginPacket()
	data[9] = 1
	_, err = parseLogin(data, capabilities)
	require.Error(t, err)
	_, err = parseLogin(loginPacket(), capabilities^capDatabase)
	require.Error(t, err)
	_, err = parseLogin(append(loginPacket(), 1), capabilities)
	require.Error(t, err)
}

func TestTextResultEncoding(t *testing.T) {
	text, err := record.NewText("unicode: café")
	require.NoError(t, err)
	result := sql.Result{Columns: []schema.Column{
		{Name: "id", Type: record.TypeInt64}, {Name: "note", Type: record.TypeText, Nullable: true}, {Name: "name", Type: record.TypeText},
	}, Rows: [][]record.Value{{record.NewInt64(-9223372036854775808), record.NewNull(), text}}}
	output, err := resultPackets("godb", result)
	require.NoError(t, err)
	require.Equal(t, []byte{3}, output[0])
	expected := appendString(nil, "-9223372036854775808")
	expected = append(expected, 0xfb)
	expected = appendString(expected, "unicode: café")
	require.Equal(t, expected, output[5])
	require.Equal(t, []byte{0xfe, 0, 0, 2, 0}, output[6])
	result.Columns[0].Type = record.TypeMap
	_, err = resultPackets("godb", result)
	require.Error(t, err)
}

func FuzzLogin(f *testing.F) {
	f.Add(loginPacket(), capabilities)
	f.Add([]byte{}, uint32(0))
	f.Fuzz(func(t *testing.T, data []byte, flags uint32) {
		if len(data) > maxPacket {
			return
		}

		info, err := parseLogin(data, flags)
		if err == nil {
			require.LessOrEqual(t, len(info.user), 128)
		}
	})
}

func FuzzPacket(f *testing.F) {
	f.Add([]byte{1, 0, 0, 0, 14})
	f.Add([]byte{255, 255, 255, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxPacket+4 {
			return
		}

		conn := &memoryConn{}
		_, err := conn.Write(data)
		require.NoError(t, err)

		payload, err := (&packets{conn: conn}).read()
		if err == nil {
			require.NotEmpty(t, payload)
			require.LessOrEqual(t, len(payload), maxPacket)
		}
	})
}

func BenchmarkTextResult(b *testing.B) {
	result := sql.Result{Columns: []schema.Column{{Name: "id", Type: record.TypeInt64}}, Rows: [][]record.Value{{record.NewInt64(1)}}}
	b.ReportAllocs()

	for b.Loop() {
		if _, err := resultPackets("godb", result); err != nil {
			b.Fatal(err)
		}
	}
}
