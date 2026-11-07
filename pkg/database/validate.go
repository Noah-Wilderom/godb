package database

import (
	"errors"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func Validate(path string) (err error) {
	if path == "" {
		return errors.New("database path must not be empty")
	}

	p, err := pager.OpenPager(path)
	if err != nil {
		return err
	}

	defer func() {
		if closeErr := p.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	_, err = inspect(p)
	return err
}

func inspect(p *pager.Pager) (*pager.DatabaseHeader, error) {
	pageZero, err := p.ReadPage(uint64(0))
	if err != nil {
		return nil, err
	}

	header, err := pager.DecodeHeader(pageZero)
	if err != nil {
		return nil, err
	}

	expectedSize := header.PageCount * uint64(header.PageSize)

	actualSize, err := p.GetFileSize()
	if err != nil {
		return nil, err
	}

	if expectedSize != actualSize {
		return nil, errors.New("database file size does not match header page count and page size")
	}

	rootPage, err := p.ReadPage(header.RootPageID)
	if err != nil {
		return nil, err
	}

	switch rootPage[0] {
	case pager.PageTypeLeaf:
		_, err := pager.DecodeLeaf(rootPage)
		if err != nil {
			return nil, err
		}

	case pager.PageTypeInternal:
		_, err := pager.DecodeInternal(rootPage)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("root page is neither a leaf nor an internal node: %w", pager.ErrPagerCorruptPage)
	}

	return header, nil
}
