package pager

import (
	"fmt"
	"os"
)

type Pager struct {
	file *os.File
}

func OpenPager(path string) (*Pager, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0o600) // #nosec G304 -- This local API intentionally opens the database path chosen by its caller.
	if err != nil {
		return nil, err
	}

	return &Pager{
		file: file,
	}, nil
}

func CreatePager(path string) (*Pager, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- This local API intentionally creates the database path chosen by its caller.
	if err != nil {
		return nil, err
	}

	return &Pager{
		file: file,
	}, nil
}

func pageOffset(pageID uint64) (int64, error) {
	if pageID > maxPageID {
		return 0, fmt.Errorf("page ID %d exceeds the supported range", pageID)
	}

	return int64(pageID) * pagerByteSize, nil
}

func (p *Pager) Close() error {
	return p.file.Close()
}

func (p *Pager) Flush() error {
	return p.file.Sync()
}

func (p *Pager) ReadPage(pageID uint64) ([]byte, error) {
	offset, err := pageOffset(pageID)
	if err != nil {
		return nil, err
	}

	buff := make([]byte, pagerByteSize)

	_, err = p.file.ReadAt(buff, offset)
	if err != nil {
		return nil, err
	}

	return buff, nil
}

func (p *Pager) WritePage(pageID uint64, page []byte) error {
	offset, err := pageOffset(pageID)
	if err != nil {
		return err
	}

	if len(page) != pagerByteSize {
		return fmt.Errorf("page size is not %d bytes", pagerByteSize)
	}

	_, err = p.file.WriteAt(page, offset)
	if err != nil {
		return err
	}

	return nil
}

func (p *Pager) AppendPage(page []byte) (uint64, error) {
	if len(page) != pagerByteSize {
		return 0, fmt.Errorf("page size is not %d bytes", pagerByteSize)
	}

	size, err := p.GetFileSize()
	if err != nil {
		return 0, err
	}

	if size%pagerByteSize != 0 {
		return 0, fmt.Errorf("file size %d is not a multiple of page size %d", size, pagerByteSize)
	}

	pageID := size / pagerByteSize

	if err := p.WritePage(pageID, page); err != nil {
		return 0, err
	}

	return pageID, nil
}

func (p *Pager) GetFileSize() (uint64, error) {
	info, err := p.file.Stat()
	if err != nil {
		return 0, err
	}

	size := info.Size()
	if size < 0 {
		return 0, fmt.Errorf("file size %d is negative", size)
	}

	return uint64(size), nil
}
