package pager

import (
	"encoding/binary"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
)

func TestHeaderIdentityRoundTripAndCorruption(t *testing.T) {
	header := NewDatabaseHeader()
	require.Equal(t, uint16(2), header.FormatVersion)
	require.NotEqual(t, uuid.Nil(), header.DatabaseID)
	require.NotEqual(t, header.DatabaseID, NewDatabaseHeader().DatabaseID)
	page, err := EncodeHeader(header)
	require.NoError(t, err)
	require.Equal(t, uint16(80), binary.LittleEndian.Uint16(page[6:8]))
	require.Equal(t, header.DatabaseID[:], page[32:48])
	decoded, err := DecodeHeader(page)
	require.NoError(t, err)
	require.Equal(t, header, decoded)

	for _, offset := range []int{32, 47, 48, 79, 80, 4095} {
		page[offset] ^= 1
		_, err := DecodeHeader(page)
		require.Error(t, err)
		page[offset] ^= 1
	}

	clear(page[:32])
	id, err := DecodeHeaderIdentity(page)
	require.NoError(t, err)
	require.Equal(t, header.DatabaseID, id)
	_, err = DecodeHeader(page)
	require.Error(t, err)
	header.DatabaseID = uuid.Nil()
	_, err = EncodeHeader(header)
	require.Error(t, err)
	header.FormatVersion = 1
	_, err = EncodeHeader(header)
	require.NoError(t, err)
	header.DatabaseID = uuid.NewV4()
	_, err = EncodeHeader(header)
	require.Error(t, err)
}
