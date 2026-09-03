package cli

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/maintenancelease"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

const maxCredentialRecoveryConfirmationBytes = 2 << 20

func isMissingCredentialLedgerRecovery(args []string) bool {
	return len(args) >= 3 && args[0] == "local" && args[1] == "config" &&
		args[2] == "recover-missing-credential-ledger"
}

func runMissingCredentialLedgerRecovery(g globals, args []string, stdout, stderr io.Writer) int {
	if g.revision >= 0 {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_revision_forbidden",
			fmt.Errorf("recovery revision is bound by the confirmation document"),
		})
	}
	fs := flag.NewFlagSet("local config recover-missing-credential-ledger", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	managementCredentialFile := fs.String("management-credential-file", "", "")
	confirmationFile := fs.String("confirmation-file", "", "")
	acceptMissingEvidence := fs.Bool("accept-missing-credential-evidence", false, "")
	if err := fs.Parse(args[3:]); err != nil {
		return writeCommandError(g, stdout, stderr, err)
	}
	if fs.NArg() != 0 {
		return writeCommandError(g, stdout, stderr, commandError{
			"cli.unknown_argument",
			fmt.Errorf("unexpected recovery arguments"),
		})
	}
	if *managementCredentialFile == "" {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_auth_required",
			fmt.Errorf("--management-credential-file is required"),
		})
	}
	if g.dryRun && (*confirmationFile != "" || *acceptMissingEvidence) {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_dry_run_flags_forbidden",
			fmt.Errorf("confirmation and acceptance flags are only valid during apply"),
		})
	}
	if !g.dryRun && *confirmationFile == "" {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_confirmation_required",
			fmt.Errorf("--confirmation-file is required during apply"),
		})
	}

	lease, err := maintenancelease.Acquire(g.configPath)
	if err != nil {
		code := "config.credential_recovery_lease_unavailable"
		if errorsIsDaemonRunning(err) {
			code = "config.credential_recovery_daemon_running"
		}
		return writeCommandError(g, stdout, stderr, commandError{code, err})
	}
	defer lease.Close()

	providedCredential, err := readCredentialFile(*managementCredentialFile)
	if err != nil {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_auth_unavailable",
			fmt.Errorf("local management credential could not be read"),
		})
	}
	expectedCredential, err := controlapi.ReadStoreToken(lease.Store(), statestore.ControlToken)
	if err != nil {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_auth_unavailable",
			fmt.Errorf("local management credential is unavailable"),
		})
	}
	if !equalRecoveryCredentials(providedCredential, expectedCredential) {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_auth_failed",
			fmt.Errorf("local management credential was rejected"),
		})
	}

	if g.dryRun {
		plan, err := configstore.InspectStoreMissingCredentialLedgerRecovery(lease.Store())
		if err != nil {
			return writeCommandError(g, stdout, stderr, err)
		}
		if err := writeOutput(g, stdout, map[string]any{
			"ok":       true,
			"dry_run":  true,
			"recovery": plan,
		}); err != nil {
			return writeCommandError(g, stdout, stderr, err)
		}
		return 0
	}

	confirmation, err := readCredentialRecoveryConfirmation(*confirmationFile)
	if err != nil {
		return writeCommandError(g, stdout, stderr, commandError{
			"config.credential_recovery_confirmation_invalid",
			fmt.Errorf("confirmation document was rejected"),
		})
	}
	result, err := configstore.RecoverStoreMissingCredentialLedger(
		lease.Store(),
		configstore.MissingCredentialLedgerRecoveryOptions{
			AcceptMissingCredentialEvidence: *acceptMissingEvidence,
			Confirmation:                    &confirmation,
		},
	)
	if err != nil {
		return writeCommandError(g, stdout, stderr, err)
	}
	if err := writeOutput(g, stdout, map[string]any{
		"ok":       true,
		"dry_run":  false,
		"recovery": result,
	}); err != nil {
		return writeCommandError(g, stdout, stderr, err)
	}
	return 0
}

func readCredentialRecoveryConfirmation(path string) (configstore.MissingCredentialLedgerRecoveryConfirmation, error) {
	payload, err := configstore.ReadProtectedFile(path, maxCredentialRecoveryConfirmationBytes)
	if err != nil {
		return configstore.MissingCredentialLedgerRecoveryConfirmation{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var confirmation configstore.MissingCredentialLedgerRecoveryConfirmation
	if err := decoder.Decode(&confirmation); err != nil {
		return configstore.MissingCredentialLedgerRecoveryConfirmation{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return configstore.MissingCredentialLedgerRecoveryConfirmation{}, fmt.Errorf("multiple JSON values")
		}
		return configstore.MissingCredentialLedgerRecoveryConfirmation{}, err
	}
	return confirmation, nil
}

func equalRecoveryCredentials(first, second string) bool {
	firstDigest := sha256.Sum256([]byte(first))
	secondDigest := sha256.Sum256([]byte(second))
	return subtle.ConstantTimeCompare(firstDigest[:], secondDigest[:]) == 1
}

func errorsIsDaemonRunning(err error) bool {
	return errors.Is(err, maintenancelease.ErrDaemonRunning)
}
