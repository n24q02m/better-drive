package cleanup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type FileJournal struct {
	Path    string
	Records []JournalRecord
}

func OpenFileJournal(path string) (*FileJournal, error) {
	if path == "" {
		return nil, errors.New("journal path is required")
	}
	journal := &FileJournal{Path: path}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		var record JournalRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("journal line %d: %w", line, err)
		}
		journal.Records = append(journal.Records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}
	if err := journal.Verify(); err != nil {
		return nil, err
	}
	return journal, nil
}

func (journal *FileJournal) Append(record JournalRecord) error {
	if journal == nil || journal.Path == "" {
		return errors.New("journal path is required")
	}
	memory := Journal{Records: append([]JournalRecord(nil), journal.Records...)}
	if err := memory.Append(record); err != nil {
		return err
	}
	record = memory.Records[len(memory.Records)-1]
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(journal.Path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	journal.Records = append(journal.Records, record)
	return nil
}

func (journal *FileJournal) Verify() error {
	if journal == nil {
		return errors.New("journal is nil")
	}
	memory := Journal{Records: journal.Records}
	return memory.Verify()
}
