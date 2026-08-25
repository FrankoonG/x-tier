package controlapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	ChallengePath = "/v1/auth/challenge"

	AuthVersion           = "xtier-control-hmac-v1"
	AuthScheme            = "XTier-HMAC-SHA256"
	ChallengeHeader       = "X-XTier-Challenge"
	ChallengeEpochHeader  = "X-XTier-Server-Epoch"
	ChallengeExpiryHeader = "X-XTier-Challenge-Expires"
	ChallengeProofHeader  = "X-XTier-Challenge-Proof"
	ResponseAuthHeader    = "X-XTier-Response-Authentication"

	maxAcceptedChallengeLifetime = 2 * time.Minute
)

const (
	challengeDomain = "xtier/control/challenge/v1"
	requestDomain   = "xtier/control/request/v1"
	responseDomain  = "xtier/control/response/v1"
)

// Challenge is a short-lived, one-time server challenge. Proof authenticates
// the challenge before a client sends a request MAC to the listener.
type Challenge struct {
	Version     string `json:"version"`
	ServerEpoch string `json:"server_epoch"`
	Nonce       string `json:"nonce"`
	ExpiresAt   string `json:"expires_at"`
	Proof       string `json:"proof"`
}

func SignChallenge(token, nonce string, expiresAt time.Time, serverEpoch ...string) (Challenge, error) {
	if _, err := decodeCanonicalHex(nonce, 32); err != nil {
		return Challenge{}, fmt.Errorf("control.challenge_nonce_invalid")
	}
	epoch := strings.Repeat("0", 64)
	if len(serverEpoch) > 1 {
		return Challenge{}, fmt.Errorf("control.challenge_epoch_invalid")
	}
	if len(serverEpoch) == 1 {
		epoch = serverEpoch[0]
	}
	if _, err := decodeCanonicalHex(epoch, 32); err != nil {
		return Challenge{}, fmt.Errorf("control.challenge_epoch_invalid")
	}
	expires := expiresAt.UTC().Format(time.RFC3339Nano)
	proof, err := authMAC(token, challengeDomain, []byte(AuthVersion), []byte(epoch), []byte(nonce), []byte(expires))
	if err != nil {
		return Challenge{}, err
	}
	return Challenge{
		Version:     AuthVersion,
		ServerEpoch: epoch,
		Nonce:       nonce,
		ExpiresAt:   expires,
		Proof:       hex.EncodeToString(proof),
	}, nil
}

func VerifyChallenge(token string, challenge Challenge, now time.Time) bool {
	if challenge.Version != AuthVersion {
		return false
	}
	if _, err := decodeCanonicalHex(challenge.ServerEpoch, 32); err != nil {
		return false
	}
	if _, err := decodeCanonicalHex(challenge.Nonce, 32); err != nil {
		return false
	}
	provided, err := decodeCanonicalHex(challenge.Proof, sha256.Size)
	if err != nil {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, challenge.ExpiresAt)
	if err != nil || !expiresAt.After(now) || expiresAt.Sub(now) > maxAcceptedChallengeLifetime {
		return false
	}
	expected, err := authMAC(token, challengeDomain, []byte(AuthVersion), []byte(challenge.ServerEpoch), []byte(challenge.Nonce), []byte(challenge.ExpiresAt))
	return err == nil && hmac.Equal(provided, expected)
}

// AuthenticateRequest installs a challenge identifier and request MAC. The
// persistent token itself is never placed in a request header or body.
func AuthenticateRequest(req *http.Request, token string, challenge Challenge, body []byte) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("control.request_invalid")
	}
	if !VerifyChallenge(token, challenge, time.Now()) {
		return fmt.Errorf("control.challenge_auth_invalid")
	}
	mac, err := requestMAC(token, challenge, req.Method, req.URL.EscapedPath(), body)
	if err != nil {
		return err
	}
	req.Header.Set(ChallengeHeader, challenge.Nonce)
	req.Header.Set(ChallengeEpochHeader, challenge.ServerEpoch)
	req.Header.Set(ChallengeExpiryHeader, challenge.ExpiresAt)
	req.Header.Set(ChallengeProofHeader, challenge.Proof)
	req.Header.Set("Authorization", AuthScheme+" "+hex.EncodeToString(mac))
	return nil
}

// RequestChallenge returns the exact signed challenge carried by a request.
// Every field must appear once and without list-style joining or whitespace.
func RequestChallenge(req *http.Request) (Challenge, bool) {
	if req == nil {
		return Challenge{}, false
	}
	nonce, ok := exactHeader(req.Header, ChallengeHeader)
	if !ok {
		return Challenge{}, false
	}
	epoch, ok := exactHeader(req.Header, ChallengeEpochHeader)
	if !ok {
		return Challenge{}, false
	}
	expires, ok := exactHeader(req.Header, ChallengeExpiryHeader)
	if !ok {
		return Challenge{}, false
	}
	proof, ok := exactHeader(req.Header, ChallengeProofHeader)
	if !ok {
		return Challenge{}, false
	}
	challenge := Challenge{Version: AuthVersion, ServerEpoch: epoch, Nonce: nonce, ExpiresAt: expires, Proof: proof}
	if _, err := decodeCanonicalHex(challenge.ServerEpoch, 32); err != nil {
		return Challenge{}, false
	}
	if _, err := decodeCanonicalHex(challenge.Nonce, 32); err != nil {
		return Challenge{}, false
	}
	if _, err := decodeCanonicalHex(challenge.Proof, sha256.Size); err != nil {
		return Challenge{}, false
	}
	if _, err := time.Parse(time.RFC3339Nano, challenge.ExpiresAt); err != nil {
		return Challenge{}, false
	}
	return challenge, true
}

// RequestNonce returns the canonical one-time nonce from a request. Multiple
// header fields and comma-joined values are rejected.
func RequestNonce(req *http.Request) (string, bool) {
	if req == nil {
		return "", false
	}
	values := req.Header.Values(ChallengeHeader)
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return "", false
	}
	nonce := strings.TrimSpace(values[0])
	if nonce != values[0] {
		return "", false
	}
	if _, err := decodeCanonicalHex(nonce, 32); err != nil {
		return "", false
	}
	return nonce, true
}

func VerifyRequestAuthentication(req *http.Request, token string, challenge Challenge, body []byte) bool {
	if req == nil || req.URL == nil || req.URL.RawQuery != "" {
		return false
	}
	carried, ok := RequestChallenge(req)
	if !ok || carried != challenge {
		return false
	}
	values := req.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	prefix := AuthScheme + " "
	if !strings.HasPrefix(values[0], prefix) || strings.Count(values[0], " ") != 1 {
		return false
	}
	provided, err := decodeCanonicalHex(strings.TrimPrefix(values[0], prefix), sha256.Size)
	if err != nil {
		return false
	}
	expected, err := requestMAC(token, challenge, req.Method, req.URL.EscapedPath(), body)
	return err == nil && hmac.Equal(provided, expected)
}

func SignResponse(header http.Header, token string, request *http.Request, requestBody []byte, status int, body []byte) error {
	if header == nil {
		return fmt.Errorf("control.response_header_nil")
	}
	mac, err := responseMAC(token, request, requestBody, status, body)
	if err != nil {
		return err
	}
	header.Set(ResponseAuthHeader, hex.EncodeToString(mac))
	return nil
}

func VerifyResponseAuthentication(header http.Header, token string, request *http.Request, requestBody []byte, status int, body []byte) bool {
	if header == nil {
		return false
	}
	values := header.Values(ResponseAuthHeader)
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	provided, err := decodeCanonicalHex(values[0], sha256.Size)
	if err != nil {
		return false
	}
	expected, err := responseMAC(token, request, requestBody, status, body)
	return err == nil && hmac.Equal(provided, expected)
}

func requestMAC(token string, challenge Challenge, method, path string, body []byte) ([]byte, error) {
	digest := sha256.Sum256(body)
	return authMAC(token, requestDomain,
		[]byte(AuthVersion), []byte(challenge.ServerEpoch), []byte(challenge.Nonce),
		[]byte(challenge.ExpiresAt), []byte(challenge.Proof), []byte(method), []byte(path), digest[:])
}

func responseMAC(token string, request *http.Request, requestBody []byte, status int, body []byte) ([]byte, error) {
	if status < 100 || status > 999 {
		return nil, fmt.Errorf("control.response_status_invalid")
	}
	if request == nil || request.URL == nil || request.URL.RawQuery != "" {
		return nil, fmt.Errorf("control.response_request_invalid")
	}
	challenge, ok := RequestChallenge(request)
	if !ok {
		return nil, fmt.Errorf("control.response_challenge_invalid")
	}
	requestDigest := sha256.Sum256(requestBody)
	responseDigest := sha256.Sum256(body)
	return authMAC(token, responseDomain,
		[]byte(AuthVersion), []byte(challenge.ServerEpoch), []byte(challenge.Nonce),
		[]byte(challenge.ExpiresAt), []byte(challenge.Proof), []byte(request.Method), []byte(request.URL.EscapedPath()),
		requestDigest[:], []byte(strconv.Itoa(status)), responseDigest[:])
}

func exactHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.Contains(values[0], ",") || strings.TrimSpace(values[0]) != values[0] {
		return "", false
	}
	return values[0], true
}

func authMAC(token, domain string, fields ...[]byte) ([]byte, error) {
	key, err := decodeToken(token)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	writeFrame(mac, []byte(domain))
	for _, field := range fields {
		writeFrame(mac, field)
	}
	return mac.Sum(nil), nil
}

type frameWriter interface {
	Write([]byte) (int, error)
}

func writeFrame(writer frameWriter, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write(value)
}

func decodeToken(token string) ([]byte, error) {
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("invalid control token")
	}
	return decoded, nil
}

func decodeCanonicalHex(value string, size int) ([]byte, error) {
	if len(value) != size*2 || strings.ToLower(value) != value {
		return nil, fmt.Errorf("invalid canonical hex")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("invalid canonical hex")
	}
	return decoded, nil
}
