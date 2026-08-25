package statestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyRecoveryCandidatesMatchOnlyOwnedHistoricalNames(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{name: "last-good", candidate: "config.json.last-good", want: true},
		{name: "timestamp-backup", candidate: "config.json.bak.20260826T010203.123456789", want: true},
		{name: "neighboring-config", candidate: "config.json.bak.notes"},
		{name: "invalid-date", candidate: "config.json.bak.20261340T999999.123456789"},
		{name: "timestamp-suffix", candidate: "config.json.bak.20260826T010203.123456789.extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := canonicalTempDir(t)
			store := openTestStore(t, filepath.Join(dir, "config.json"))
			defer store.Close()
			if err := os.WriteFile(filepath.Join(dir, test.candidate), []byte("candidate"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := store.HasLegacyRecoveryCandidates()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("HasLegacyRecoveryCandidates()=%t, want %t", got, test.want)
			}
		})
	}
}
