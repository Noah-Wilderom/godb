package mysqlwire

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"time"
)

const (
	capLongPassword uint32 = 1
	capFoundRows    uint32 = 1 << 1
	capLongFlag     uint32 = 1 << 2
	capDatabase     uint32 = 1 << 3
	capProtocol41   uint32 = 1 << 9
	capSSL          uint32 = 1 << 11
	capSecure       uint32 = 1 << 15
	capPlugin       uint32 = 1 << 19
	capabilities           = capLongPassword | capFoundRows | capLongFlag | capDatabase | capProtocol41 | capSSL | capSecure | capPlugin
	authPlugin             = "caching_sha2_password"
)

func greeting(id uint32, seed []byte) []byte {
	payload := append([]byte{10}, serverVersion...)
	payload = append(payload, 0)
	payload = binary.LittleEndian.AppendUint32(payload, id)
	payload = append(payload, seed[:8]...)
	payload = append(payload, 0)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(capabilities&65535))
	payload = append(payload, 46, 2, 0)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(capabilities>>16))
	payload = append(payload, 21)
	payload = append(payload, make([]byte, 10)...)
	payload = append(payload, seed[8:]...)
	payload = append(payload, 0)
	payload = append(payload, authPlugin...)
	return append(payload, 0)
}

type login struct{ user, database, plugin string }

func parseLogin(data []byte, sslFlags uint32) (login, error) {
	var result login

	if len(data) < 32 {
		return result, errProtocol
	}

	flags := binary.LittleEndian.Uint32(data)
	if flags != sslFlags || flags&capProtocol41 == 0 || flags&capSecure == 0 || flags&capPlugin == 0 || flags&capSSL == 0 {
		return result, errProtocol
	}

	if data[8] != 45 && data[8] != 46 && data[8] != 255 {
		return result, errProtocol
	}

	if !bytes.Equal(data[9:32], make([]byte, 23)) {
		return result, errProtocol
	}

	remaining := data[32:]

	var err error

	result.user, remaining, err = readCString(remaining)
	if err != nil || len(result.user) > 128 || len(remaining) == 0 {
		return result, errProtocol
	}

	authSize := int(remaining[0])

	remaining = remaining[1:]
	if authSize > len(remaining) {
		return result, errProtocol
	}

	remaining = remaining[authSize:]
	if flags&capDatabase != 0 {
		result.database, remaining, err = readCString(remaining)
		if err != nil {
			return result, err
		}
	}

	result.plugin, remaining, err = readCString(remaining)
	if err != nil || len(remaining) != 0 {
		return result, errProtocol
	}

	return result, nil
}

func (e *Endpoint) authenticate(ctx context.Context, p *packets, id uint32) error {
	if err := p.conn.SetDeadline(time.Now().Add(e.config.HandshakeTimeout)); err != nil {
		return err
	}

	seed := make([]byte, 20)
	if _, err := rand.Read(seed); err != nil {
		return err
	}

	for i := range seed {
		if seed[i] == 0 {
			seed[i] = 1
		}
	}

	if err := p.write(greeting(id, seed)); err != nil {
		return err
	}

	request, err := p.read()
	if err != nil {
		return err
	}

	if len(request) != 32 || binary.LittleEndian.Uint32(request)&capSSL == 0 {
		_ = p.writeError(&Error{3159, "HY000", "TLS is required"})
		return errProtocol
	}

	flags := binary.LittleEndian.Uint32(request)

	secured := tls.Server(p.conn, e.config.TLS)
	if err := secured.HandshakeContext(ctx); err != nil {
		return err
	}

	p.conn = secured

	response, err := p.read()
	if err != nil {
		return err
	}

	info, err := parseLogin(response, flags)
	if err != nil {
		_ = p.writeError(err)
		return err
	}

	p.foundRows = flags&capFoundRows != 0

	if info.plugin != authPlugin {
		switchPacket := append([]byte{0xfe}, authPlugin...)
		switchPacket = append(switchPacket, 0)

		switchPacket = append(switchPacket, seed...)
		if err := p.write(append(switchPacket, 0)); err != nil {
			return err
		}

		if _, err := p.read(); err != nil {
			return err
		}
	}

	if err := p.write([]byte{1, 4}); err != nil {
		return err
	}

	password, err := p.read()
	if err != nil {
		return err
	}

	valid := len(password) > 1 && len(password) <= 4097 && password[len(password)-1] == 0

	var digest [sha256.Size]byte

	if valid {
		digest = sha256.Sum256(password[:len(password)-1])
	}

	clear(password)

	if !valid || subtle.ConstantTimeCompare(digest[:], e.config.PasswordHash[:]) != 1 || info.user != e.config.User {
		denied := &Error{1045, "28000", "Access denied"}
		_ = p.writeError(denied)
		return denied
	}

	if info.database != "" && info.database != e.config.Database {
		denied := &Error{1049, "42000", "Unknown database"}
		_ = p.writeError(denied)
		return denied
	}

	return p.ok(0)
}
