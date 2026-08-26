package state

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	metaBucket    = []byte("meta")
	attemptBucket = []byte("attempts")
	keyGeneration = []byte("desired-generation")
	keyEpoch      = []byte("event-epoch")
	keyReportSeq  = []byte("report-sequence")
	keyWatermark  = []byte("event-watermark")
)

type Store struct {
	db *bolt.DB
}

type Attempt struct {
	Generation    uint64 `json:"generation"`
	ReleaseDigest string `json:"releaseDigest"`
	State         string `json:"state"`
	Intent        string `json:"intent,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
}

func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second, NoFreelistSync: false})
	if err != nil {
		return nil, fmt.Errorf("open player state: %w", err)
	}
	store := &Store{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(metaBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(attemptBucket)
		return err
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize player state: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) AcceptGeneration(generation uint64, digest string) (bool, error) {
	if generation == 0 || digest == "" {
		return false, errors.New("generation and digest are required")
	}
	accepted := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		current := readUint64(meta.Get(keyGeneration))
		if generation <= current {
			return nil
		}
		attempt := Attempt{Generation: generation, ReleaseDigest: digest, State: "PENDING"}
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return err
		}
		if err := tx.Bucket(attemptBucket).Put(uint64Key(generation), encoded); err != nil {
			return err
		}
		if err := meta.Put(keyGeneration, uint64Key(generation)); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}

func (s *Store) RecordTransition(generation uint64, state, intent, outcome string) error {
	if generation == 0 || state == "" {
		return errors.New("generation and state are required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(attemptBucket)
		raw := bucket.Get(uint64Key(generation))
		if raw == nil {
			return errors.New("unknown assignment generation")
		}
		var attempt Attempt
		if err := json.Unmarshal(raw, &attempt); err != nil {
			return err
		}
		attempt.State = state
		attempt.Intent = intent
		attempt.Outcome = outcome
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return err
		}
		return bucket.Put(uint64Key(generation), encoded)
	})
}

func (s *Store) Attempt(generation uint64) (Attempt, error) {
	var attempt Attempt
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(attemptBucket).Get(uint64Key(generation))
		if raw == nil {
			return errors.New("unknown assignment generation")
		}
		return json.Unmarshal(raw, &attempt)
	})
	return attempt, err
}

func (s *Store) SetEventProgress(epoch string, reportSequence, watermark uint64) error {
	if epoch == "" {
		return errors.New("event epoch is required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if err := meta.Put(keyEpoch, []byte(epoch)); err != nil {
			return err
		}
		if reportSequence < readUint64(meta.Get(keyReportSeq)) || watermark < readUint64(meta.Get(keyWatermark)) {
			return errors.New("event progress cannot move backwards")
		}
		if err := meta.Put(keyReportSeq, uint64Key(reportSequence)); err != nil {
			return err
		}
		return meta.Put(keyWatermark, uint64Key(watermark))
	})
}

func (s *Store) EventProgress() (epoch string, reportSequence, watermark uint64, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		epoch = string(meta.Get(keyEpoch))
		reportSequence = readUint64(meta.Get(keyReportSeq))
		watermark = readUint64(meta.Get(keyWatermark))
		return nil
	})
	return
}

func uint64Key(value uint64) []byte {
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, value)
	return result
}

func readUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
