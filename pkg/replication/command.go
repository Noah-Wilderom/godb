package replication

import (
	"bytes"
	"fmt"
	"uuid"

	"github.com/Noah-Wilderom/godb/pkg/database"
)

const (
	commandMagic      = "GODBRPL1"
	commandHeaderSize = 24
	MaxProposalBytes  = commandHeaderSize + database.MaxCommandBytes
)

func EncodeProposal(requestID uuid.UUID, command []byte) ([]byte, error) {
	if requestID == uuid.Nil() {
		return nil, ErrInvalidProposal
	}

	if _, err := database.DecodeCommand(command); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidProposal, err)
	}

	encoded := make([]byte, commandHeaderSize, commandHeaderSize+len(command))
	copy(encoded, commandMagic)
	copy(encoded[8:24], requestID[:])
	return append(encoded, command...), nil
}

func DecodeProposal(encoded []byte) (uuid.UUID, []byte, error) {
	if len(encoded) <= commandHeaderSize || len(encoded) > MaxProposalBytes || string(encoded[:8]) != commandMagic {
		return uuid.Nil(), nil, ErrInvalidProposal
	}

	var requestID uuid.UUID

	copy(requestID[:], encoded[8:24])

	if requestID == uuid.Nil() {
		return uuid.Nil(), nil, ErrInvalidProposal
	}

	command := encoded[commandHeaderSize:]
	if _, err := database.DecodeCommand(command); err != nil {
		return uuid.Nil(), nil, fmt.Errorf("%w: %w", ErrInvalidProposal, err)
	}

	return requestID, bytes.Clone(command), nil
}
