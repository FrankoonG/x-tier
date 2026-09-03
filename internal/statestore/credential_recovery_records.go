package statestore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	credentialRecoveryRecordPrefix = "credential-recovery"
	credentialRecoveryForensicKind = "forensic"
	credentialRecoveryAuditKind    = "audit"
	credentialRecoveryNonceBytes   = 16
)

// CreateCredentialRecoveryForensic creates an immutable forensic record and
// returns its store-owned name.
func (s *Store) CreateCredentialRecoveryForensic(data []byte) (string, error) {
	return s.createCredentialRecoveryRecord(credentialRecoveryForensicKind, data)
}

// CreateCredentialRecoveryAudit creates an immutable audit record and returns
// its store-owned name.
func (s *Store) CreateCredentialRecoveryAudit(data []byte) (string, error) {
	return s.createCredentialRecoveryRecord(credentialRecoveryAuditKind, data)
}

// CredentialRecoveryRecordNames returns immutable forensic and audit records
// owned by this store.
func (s *Store) CredentialRecoveryRecordNames() ([]string, error) {
	return s.credentialRecoveryRecordNames()
}

// ReadCredentialRecoveryRecord reads one record selected from
// CredentialRecoveryRecordNames. The caller supplies the maximum size.
func (s *Store) ReadCredentialRecoveryRecord(name string, limit int64) ([]byte, error) {
	return s.readCredentialRecoveryRecord(name, limit)
}

func (s *Store) createCredentialRecoveryRecord(kind string, data []byte) (string, error) {
	if s == nil || s.state == nil {
		return "", fmt.Errorf("statestore.closed")
	}
	for range tempAttempts {
		var nonce [credentialRecoveryNonceBytes]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", fmt.Errorf("statestore.credential_recovery_random: %w", err)
		}
		name := credentialRecoveryRecordName(kind, time.Now().UTC(), nonce[:])
		if err := createExclusiveInRoot(s.state, s.stateDir, name, data); err == nil {
			return name, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fs.ErrExist
}

func (s *Store) credentialRecoveryRecordNames() ([]string, error) {
	if s == nil || s.state == nil {
		return nil, fmt.Errorf("statestore.closed")
	}
	entries, err := fs.ReadDir(s.state.FS(), ".")
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !validCredentialRecoveryRecordNameOfAnyKind(name) {
			continue
		}
		file, err := openSecureRootFile(s.state, name, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Store) readCredentialRecoveryRecord(name string, limit int64) ([]byte, error) {
	if s == nil || s.state == nil {
		return nil, fmt.Errorf("statestore.closed")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("statestore.read_limit_invalid")
	}
	if !validCredentialRecoveryRecordNameOfAnyKind(name) {
		return nil, fmt.Errorf("statestore.credential_recovery_name_invalid")
	}
	return readFromRoot(s.state, name, limit)
}

func validCredentialRecoveryRecordNameOfAnyKind(name string) bool {
	return validCredentialRecoveryRecordName(credentialRecoveryForensicKind, name) ||
		validCredentialRecoveryRecordName(credentialRecoveryAuditKind, name)
}

func credentialRecoveryRecordName(kind string, created time.Time, nonce []byte) string {
	return credentialRecoveryRecordPrefix + "." + kind + "." +
		created.UTC().Format("20060102T150405.000000000") + "." +
		hex.EncodeToString(nonce) + ".json"
}

func validCredentialRecoveryRecordName(kind, name string) bool {
	if kind != credentialRecoveryForensicKind && kind != credentialRecoveryAuditKind {
		return false
	}
	parts := strings.Split(name, ".")
	if len(parts) != 6 || parts[0] != credentialRecoveryRecordPrefix || parts[1] != kind || parts[5] != "json" {
		return false
	}
	if len(parts[2]) != len("20060102T150405") || len(parts[3]) != 9 {
		return false
	}
	if _, err := time.Parse("20060102T150405.000000000", parts[2]+"."+parts[3]); err != nil {
		return false
	}
	if len(parts[4]) != credentialRecoveryNonceBytes*2 || parts[4] != strings.ToLower(parts[4]) {
		return false
	}
	_, err := hex.DecodeString(parts[4])
	return err == nil
}
