package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

type errorReader struct{}

func (e *errorReader) Read(p []byte) (int, error) {
	return 0, errors.New("read error")
}

func TestGenerateChecksum(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		r := strings.NewReader("")
		got, err := GenerateChecksum(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := hex.EncodeToString(sha256.New().Sum(nil))
		if got != want {
			t.Errorf("checksum mismatch: got %s, want %s", got, want)
		}
	})

	t.Run("known string", func(t *testing.T) {
		input := "hello world"
		r := strings.NewReader(input)
		got, err := GenerateChecksum(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sum := sha256.Sum256([]byte(input))
		want := hex.EncodeToString(sum[:])
		if got != want {
			t.Errorf("checksum mismatch: got %s, want %s", got, want)
		}
	})

	t.Run("large input", func(t *testing.T) {
		data := bytes.Repeat([]byte("a"), 1024*1024)
		r := bytes.NewReader(data)
		got, err := GenerateChecksum(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sum := sha256.Sum256(data)
		want := hex.EncodeToString(sum[:])
		if got != want {
			t.Errorf("checksum mismatch: got %s, want %s", got, want)
		}
	})

	t.Run("reader error", func(t *testing.T) {
		errReader := &errorReader{}
		_, err := GenerateChecksum(errReader)
		if err == nil {
			t.Error("expected error, got nil")
		}
	})

	t.Run("nil reader", func(t *testing.T) {
		_, err := GenerateChecksum(nil)
		if err == nil {
			t.Error("expected error, got nil")
		}
	})
}
