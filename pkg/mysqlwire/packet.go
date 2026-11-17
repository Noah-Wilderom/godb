package mysqlwire

import (
	"encoding/binary"
	"errors"
	"io"
	"net"

	"github.com/Noah-Wilderom/godb/pkg/sql"
)

const (
	maxPacket = 128 << 10
)

var errProtocol = errors.New("invalid or unsupported MySQL packet")

type packets struct {
	conn      net.Conn
	sequence  byte
	foundRows bool
}

func (p *packets) read() ([]byte, error) {
	var header [4]byte

	if _, err := io.ReadFull(p.conn, header[:]); err != nil {
		return nil, err
	}

	size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if header[3] != p.sequence || size == 0 || size > maxPacket {
		return nil, errProtocol
	}

	p.sequence++
	payload := make([]byte, size)
	_, err := io.ReadFull(p.conn, payload)
	return payload, err
}

func (p *packets) write(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxPacket {
		return errProtocol
	}

	size := len(payload)
	header := []byte{byte(size & 255), byte((size >> 8) & 255), byte((size >> 16) & 255), p.sequence}
	p.sequence++

	for _, data := range [][]byte{header, payload} {
		for len(data) > 0 {
			n, err := p.conn.Write(data)
			if err != nil {
				return err
			}

			if n == 0 {
				return io.ErrShortWrite
			}

			data = data[n:]
		}
	}

	return nil
}

func appendNumber(dst []byte, n uint64) []byte {
	switch {
	case n < 251:
		return append(dst, byte(n))
	case n <= 65535:
		return binary.LittleEndian.AppendUint16(append(dst, 0xfc), uint16(n))
	default:
		return binary.LittleEndian.AppendUint64(append(dst, 0xfe), n)
	}
}

func appendString(dst []byte, s string) []byte {
	return append(appendNumber(dst, uint64(len(s))), s...)
}

func readCString(data []byte) (string, []byte, error) {
	for i, b := range data {
		if b == 0 {
			return string(data[:i]), data[i+1:], nil
		}
	}

	return "", nil, errProtocol
}

type Error struct {
	Code           uint16
	State, Message string
}

func (e *Error) Error() string { return e.Message }
func (p *packets) writeError(err error) error {
	var wire *Error

	if !errors.As(err, &wire) {
		wire = &Error{1105, "HY000", "GoDB request failed"}
	}

	state := wire.State
	if len(state) != 5 {
		state = "HY000"
	}

	payload := binary.LittleEndian.AppendUint16([]byte{0xff}, wire.Code)
	payload = append(payload, '#')
	payload = append(payload, state...)
	return p.write(append(payload, wire.Message...))
}

func (p *packets) ok(affected uint64) error {
	payload := appendNumber([]byte{0}, affected)
	payload = appendNumber(payload, 0)
	return p.write(append(payload, 2, 0, 0, 0))
}

func (p *packets) resultOK(result sql.Result) error {
	if p.foundRows {
		return p.ok(result.MatchedRows)
	}

	return p.ok(result.AffectedRows)
}
