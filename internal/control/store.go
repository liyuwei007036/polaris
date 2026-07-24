package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/security"
	_ "modernc.org/sqlite"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrForbidden          = errors.New("forbidden")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("conflict")
)

const (
	masterKeyFile       = "master.key"
	defaultSessionTTL   = 8 * time.Hour
	defaultTokenTTL     = 15 * time.Minute
	maximumTokenTTL     = time.Hour
	registrationPollTTL = 24 * time.Hour
)

type Store struct {
	db                *sql.DB
	dataDir           string
	masterKey         []byte
	releaseSigningKey ed25519.PrivateKey
}

type Operator struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at,omitempty"`
	LastLoginAt string `json:"last_login_at,omitempty"`
}

type Session struct {
	Token      string
	CSRFToken  string
	OperatorID string
	Role       string
	ExpiresAt  time.Time
}

type RegistrationToken struct {
	Token     string
	ExpiresAt time.Time
}

type Registration struct {
	ID          string
	PollToken   string
	NodeName    string
	Status      string
	NodeID      string
	Certificate []byte
	CAPEM       []byte
}

type RegistrationInput struct {
	Token        string
	NodeName     string
	CSRPEM       []byte
	Capabilities string
}

type Node struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AgentVersion string `json:"agent_version"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	SingBox      string `json:"sing_box_version"`
	Capabilities string `json:"capabilities"`
	Online       bool   `json:"online"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
}

type AgentStatus struct {
	AgentVersion string
	OS           string
	Architecture string
	SingBox      string
	Capabilities string
	Metrics      string
}

type PendingRegistration struct {
	ID           string `json:"id"`
	NodeName     string `json:"node_name"`
	Capabilities string `json:"capabilities"`
	CreatedAt    string `json:"created_at"`
}

// Task is a structured, idempotent instruction. The payload is never shell
// text; its shape is defined by the task kind and validated by both roles.
type Task struct {
	ID             string `json:"id"`
	NodeID         string `json:"node_id"`
	OperatorID     string `json:"operator_id,omitempty"`
	Kind           string `json:"kind"`
	IdempotencyKey string `json:"idempotency_key"`
	Payload        string `json:"payload"`
	ExpectedHash   string `json:"expected_hash"`
	Status         string `json:"status"`
	ResultSummary  string `json:"result_summary,omitempty"`
	CreatedAt      string `json:"created_at"`
	StartedAt      string `json:"started_at,omitempty"`
	FinishedAt     string `json:"finished_at,omitempty"`
}

type AuditEvent struct {
	ID         string `json:"id"`
	OperatorID string `json:"operator_id"`
	Email      string `json:"operator_email"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Summary    string `json:"summary"`
	CreatedAt  string `json:"created_at"`
}

type Listener struct {
	ID          string       `json:"id"`
	NodeID      string       `json:"node_id"`
	Name        string       `json:"name"`
	ListenAddr  string       `json:"listen_address"`
	Port        uint16       `json:"port"`
	BackendPort uint16       `json:"backend_port"`
	Enabled     bool         `json:"enabled"`
	Spec        ProtocolSpec `json:"spec"`
}

type Endpoint struct {
	ID         string `json:"id"`
	ListenerID string `json:"listener_id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
}

func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	key, err := loadOrCreateMasterKey(dataDir)
	if err != nil {
		return nil, err
	}
	releaseSigningKey, err := loadOrCreateReleaseSigningKey(dataDir)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "sb-control.db"))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, dataDir: dataDir, masterKey: key, releaseSigningKey: releaseSigningKey}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DataDir() string { return s.dataDir }

func (s *Store) MasterKey() []byte {
	key := make([]byte, len(s.masterKey))
	copy(key, s.masterKey)
	return key
}

func (s *Store) CreateInitialAdmin(ctx context.Context, email, password string) (secret string, err error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operators").Scan(&count); err != nil {
		return "", fmt.Errorf("count operators: %w", err)
	}
	if count > 0 {
		return "", ErrConflict
	}
	_, createdSecret, createErr := s.createOperator(ctx, email, password, "admin")
	return createdSecret, createErr
}

func validateOperatorInput(email, role string) error {
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 320 {
		return errors.New("operator email is invalid")
	}
	if role != "admin" && role != "operator" && role != "viewer" {
		return errors.New("operator role must be admin, operator, or viewer")
	}
	return nil
}

func (s *Store) createOperator(ctx context.Context, email, password, role string) (Operator, string, error) {
	if err := validateOperatorInput(email, role); err != nil {
		return Operator{}, "", err
	}
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		return Operator{}, "", err
	}
	secret, err := security.NewTOTPSecret()
	if err != nil {
		return Operator{}, "", err
	}
	encryptedSecret, err := security.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return Operator{}, "", fmt.Errorf("encrypt TOTP secret: %w", err)
	}
	identifier, err := newID()
	if err != nil {
		return Operator{}, "", err
	}
	createdAt := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO operators (id, email, password_hash, totp_secret, role, enabled, created_at, last_login_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, NULL)`, identifier, email, passwordHash, encryptedSecret, role, createdAt)
	if err != nil {
		return Operator{}, "", fmt.Errorf("create operator: %w", err)
	}
	return Operator{ID: identifier, Email: email, Role: role, Enabled: true, CreatedAt: time.Unix(createdAt, 0).UTC().Format(time.RFC3339)}, secret, nil
}

func (s *Store) CreateOperator(ctx context.Context, email, password, role string) (Operator, string, error) {
	return s.createOperator(ctx, email, password, role)
}

func (s *Store) ListOperators(ctx context.Context) ([]Operator, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, email, role, enabled, created_at, last_login_at FROM operators ORDER BY email, id`)
	if err != nil {
		return nil, fmt.Errorf("list operators: %w", err)
	}
	defer rows.Close()
	var operators []Operator
	for rows.Next() {
		var operator Operator
		var createdAt int64
		var lastLogin sql.NullInt64
		if err := rows.Scan(&operator.ID, &operator.Email, &operator.Role, &operator.Enabled, &createdAt, &lastLogin); err != nil {
			return nil, fmt.Errorf("read operator: %w", err)
		}
		operator.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		if lastLogin.Valid {
			operator.LastLoginAt = time.Unix(lastLogin.Int64, 0).UTC().Format(time.RFC3339)
		}
		operators = append(operators, operator)
	}
	return operators, rows.Err()
}

func (s *Store) UpdateOperator(ctx context.Context, operator Operator) (Operator, error) {
	if operator.ID == "" {
		return Operator{}, errors.New("operator ID is required")
	}
	if operator.Role != "admin" && operator.Role != "operator" && operator.Role != "viewer" {
		return Operator{}, errors.New("operator role must be admin, operator, or viewer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operator{}, fmt.Errorf("start operator update: %w", err)
	}
	defer tx.Rollback()
	var existingRole string
	var existingEnabled bool
	err = tx.QueryRowContext(ctx, `SELECT role, enabled FROM operators WHERE id = ?`, operator.ID).Scan(&existingRole, &existingEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Operator{}, ErrNotFound
	}
	if err != nil {
		return Operator{}, fmt.Errorf("load operator: %w", err)
	}
	if existingRole == "admin" && existingEnabled && (operator.Role != "admin" || !operator.Enabled) {
		var otherAdmins int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operators WHERE role = 'admin' AND enabled = 1 AND id <> ?`, operator.ID).Scan(&otherAdmins); err != nil {
			return Operator{}, fmt.Errorf("count remaining administrators: %w", err)
		}
		if otherAdmins == 0 {
			return Operator{}, errors.New("cannot disable or demote the last enabled administrator")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operators SET role = ?, enabled = ? WHERE id = ?`, operator.Role, operator.Enabled, operator.ID); err != nil {
		return Operator{}, fmt.Errorf("update operator: %w", err)
	}
	if !operator.Enabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE operator_id = ?`, operator.ID); err != nil {
			return Operator{}, fmt.Errorf("remove disabled operator sessions: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Operator{}, fmt.Errorf("commit operator update: %w", err)
	}
	updated, err := s.operatorByID(ctx, operator.ID)
	if err != nil {
		return Operator{}, err
	}
	return updated, nil
}

func (s *Store) SetOperatorPassword(ctx context.Context, operatorID, password string) error {
	passwordHash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE operators SET password_hash = ? WHERE id = ?`, passwordHash, operatorID)
	if err != nil {
		return fmt.Errorf("set operator password: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read operator password update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ResetOperatorTOTP(ctx context.Context, operatorID string) (string, error) {
	secret, err := security.NewTOTPSecret()
	if err != nil {
		return "", err
	}
	encrypted, err := security.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return "", fmt.Errorf("encrypt TOTP secret: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("start MFA reset: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE operators SET totp_secret = ? WHERE id = ?`, encrypted, operatorID)
	if err != nil {
		return "", fmt.Errorf("reset operator MFA: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("read operator MFA reset: %w", err)
	}
	if changed != 1 {
		return "", ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE operator_id = ?`, operatorID); err != nil {
		return "", fmt.Errorf("remove operator login challenges: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE operator_id = ?`, operatorID); err != nil {
		return "", fmt.Errorf("remove operator sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit MFA reset: %w", err)
	}
	return secret, nil
}

func (s *Store) operatorByID(ctx context.Context, operatorID string) (Operator, error) {
	var operator Operator
	var createdAt int64
	var lastLogin sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, email, role, enabled, created_at, last_login_at FROM operators WHERE id = ?`, operatorID).
		Scan(&operator.ID, &operator.Email, &operator.Role, &operator.Enabled, &createdAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return Operator{}, ErrNotFound
	}
	if err != nil {
		return Operator{}, fmt.Errorf("load operator: %w", err)
	}
	operator.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	if lastLogin.Valid {
		operator.LastLoginAt = time.Unix(lastLogin.Int64, 0).UTC().Format(time.RFC3339)
	}
	return operator, nil
}

func (s *Store) StartLogin(ctx context.Context, email, password string) (string, error) {
	var operatorID, passwordHash string
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash, enabled FROM operators WHERE email = ?`, email).Scan(&operatorID, &passwordHash, &enabled)
	if errors.Is(err, sql.ErrNoRows) || !enabled {
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("load operator: %w", err)
	}
	ok, err := security.VerifyPassword(passwordHash, password)
	if err != nil || !ok {
		return "", ErrInvalidCredentials
	}
	challengeID, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO login_challenges (id_hash, operator_id, expires_at, used_at)
		VALUES (?, ?, ?, NULL)`, security.TokenHash(challengeID), operatorID, time.Now().UTC().Add(5*time.Minute).Unix())
	if err != nil {
		return "", fmt.Errorf("create MFA challenge: %w", err)
	}
	return challengeID, nil
}

func (s *Store) FinishLogin(ctx context.Context, challengeID, code string) (Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("start MFA verification: %w", err)
	}
	defer tx.Rollback()
	var operatorID, role string
	var encryptedSecret []byte
	var expiresAt int64
	err = tx.QueryRowContext(ctx, `SELECT c.operator_id, o.role, o.totp_secret, c.expires_at
		FROM login_challenges c JOIN operators o ON o.id = c.operator_id
		WHERE c.id_hash = ? AND c.used_at IS NULL AND o.enabled = 1`, security.TokenHash(challengeID)).Scan(&operatorID, &role, &encryptedSecret, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) || time.Now().UTC().Unix() > expiresAt {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, fmt.Errorf("load MFA challenge: %w", err)
	}
	secret, err := security.Decrypt(s.masterKey, encryptedSecret)
	if err != nil || !security.VerifyTOTP(string(secret), code, time.Now().UTC()) {
		return Session{}, ErrInvalidCredentials
	}
	updated, err := tx.ExecContext(ctx, `UPDATE login_challenges SET used_at = ? WHERE id_hash = ? AND used_at IS NULL`, nowUnix(), security.TokenHash(challengeID))
	if err != nil {
		return Session{}, fmt.Errorf("consume MFA challenge: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil || changed != 1 {
		return Session{}, ErrInvalidCredentials
	}
	sessionToken, err := security.RandomToken(32)
	if err != nil {
		return Session{}, err
	}
	csrfToken, err := security.RandomToken(32)
	if err != nil {
		return Session{}, err
	}
	expires := time.Now().UTC().Add(defaultSessionTTL)
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions (id_hash, operator_id, csrf_hash, expires_at, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)`, security.TokenHash(sessionToken), operatorID, security.TokenHash(csrfToken), expires.Unix(), nowUnix(), nowUnix())
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operators SET last_login_at = ? WHERE id = ?`, nowUnix(), operatorID); err != nil {
		return Session{}, fmt.Errorf("record operator login: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit MFA verification: %w", err)
	}
	return Session{Token: sessionToken, CSRFToken: csrfToken, OperatorID: operatorID, Role: role, ExpiresAt: expires}, nil
}

func (s *Store) Authenticate(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (Operator, error) {
	if sessionToken == "" {
		return Operator{}, ErrUnauthorized
	}
	var operator Operator
	var csrfHash []byte
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT o.id, o.email, o.role, o.enabled, s.csrf_hash, s.expires_at
		FROM sessions s JOIN operators o ON o.id = s.operator_id
		WHERE s.id_hash = ?`, security.TokenHash(sessionToken)).Scan(&operator.ID, &operator.Email, &operator.Role, &operator.Enabled, &csrfHash, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) || !operator.Enabled || time.Now().UTC().Unix() > expiresAt {
		return Operator{}, ErrUnauthorized
	}
	if err != nil {
		return Operator{}, fmt.Errorf("load session: %w", err)
	}
	if requireCSRF {
		if csrfToken == "" || !constantTimeEqual(csrfHash, security.TokenHash(csrfToken)) {
			return Operator{}, ErrForbidden
		}
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, nowUnix(), security.TokenHash(sessionToken))
	return operator, nil
}

func (s *Store) DeleteSession(ctx context.Context, sessionToken string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, security.TokenHash(sessionToken))
	return err
}

func (s *Store) CreateRegistrationToken(ctx context.Context, operatorID string, ttl time.Duration) (RegistrationToken, error) {
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	if ttl > maximumTokenTTL {
		return RegistrationToken{}, fmt.Errorf("registration token lifetime must not exceed %s", maximumTokenTTL)
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return RegistrationToken{}, err
	}
	id, err := newID()
	if err != nil {
		return RegistrationToken{}, err
	}
	expiresAt := time.Now().UTC().Add(ttl)
	_, err = s.db.ExecContext(ctx, `INSERT INTO registration_tokens (id, token_hash, created_by, expires_at, used_at, created_at)
		VALUES (?, ?, ?, ?, NULL, ?)`, id, security.TokenHash(token), operatorID, expiresAt.Unix(), nowUnix())
	if err != nil {
		return RegistrationToken{}, fmt.Errorf("create registration token: %w", err)
	}
	return RegistrationToken{Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Store) RegisterAgent(ctx context.Context, input RegistrationInput) (Registration, error) {
	if input.Token == "" || input.NodeName == "" || len(input.CSRPEM) == 0 {
		return Registration{}, errors.New("registration token, node name and CSR are required")
	}
	if len(input.NodeName) > 128 || len(input.Capabilities) > 32*1024 {
		return Registration{}, errors.New("registration input exceeds allowed size")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, fmt.Errorf("start registration: %w", err)
	}
	defer tx.Rollback()
	var tokenID string
	var expiresAt int64
	err = tx.QueryRowContext(ctx, `SELECT id, expires_at FROM registration_tokens WHERE token_hash = ? AND used_at IS NULL`, security.TokenHash(input.Token)).Scan(&tokenID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) || time.Now().UTC().Unix() > expiresAt {
		return Registration{}, ErrUnauthorized
	}
	if err != nil {
		return Registration{}, fmt.Errorf("load registration token: %w", err)
	}
	registrationID, err := newID()
	if err != nil {
		return Registration{}, err
	}
	pollToken, err := security.RandomToken(32)
	if err != nil {
		return Registration{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE registration_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL`, nowUnix(), tokenID)
	if err != nil {
		return Registration{}, fmt.Errorf("consume registration token: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil || changed != 1 {
		return Registration{}, ErrUnauthorized
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO registrations
		(id, poll_hash, node_name, csr_pem, capabilities, status, node_id, expires_at, created_at, approved_at)
		VALUES (?, ?, ?, ?, ?, 'pending', NULL, ?, ?, NULL)`, registrationID, security.TokenHash(pollToken), input.NodeName, input.CSRPEM, input.Capabilities, time.Now().UTC().Add(registrationPollTTL).Unix(), nowUnix())
	if err != nil {
		return Registration{}, fmt.Errorf("create registration request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Registration{}, fmt.Errorf("commit registration: %w", err)
	}
	return Registration{ID: registrationID, PollToken: pollToken, NodeName: input.NodeName, Status: "pending"}, nil
}

func (s *Store) ApproveRegistration(ctx context.Context, registrationID string, ca *CertificateAuthority) (Registration, error) {
	var nodeName string
	var csrPEM []byte
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT node_name, csr_pem, status FROM registrations WHERE id = ?`, registrationID).Scan(&nodeName, &csrPEM, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, ErrNotFound
	}
	if err != nil {
		return Registration{}, fmt.Errorf("load registration request: %w", err)
	}
	if status != "pending" {
		return Registration{}, ErrConflict
	}
	nodeID, err := newID()
	if err != nil {
		return Registration{}, err
	}
	certificate, serial, err := ca.SignNodeCSR(csrPEM, nodeID, nodeName)
	if err != nil {
		return Registration{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, fmt.Errorf("start registration approval: %w", err)
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `UPDATE registrations SET status = 'approved', node_id = ?, approved_at = ? WHERE id = ? AND status = 'pending'`, nodeID, nowUnix(), registrationID)
	if err != nil {
		return Registration{}, fmt.Errorf("approve registration: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil || changed != 1 {
		return Registration{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO nodes (id, name, certificate_pem, certificate_serial, revoked_at, created_at)
		VALUES (?, ?, ?, ?, NULL, ?)`, nodeID, nodeName, certificate, serial, nowUnix())
	if err != nil {
		return Registration{}, fmt.Errorf("create node: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO certificates (serial, node_id, certificate_pem, revoked_at, issued_at)
		VALUES (?, ?, ?, NULL, ?)`, serial, nodeID, certificate, nowUnix())
	if err != nil {
		return Registration{}, fmt.Errorf("record node certificate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Registration{}, fmt.Errorf("commit registration approval: %w", err)
	}
	return Registration{ID: registrationID, NodeName: nodeName, Status: "approved", NodeID: nodeID, Certificate: certificate, CAPEM: ca.CertificatePEM()}, nil
}

func (s *Store) PollRegistration(ctx context.Context, registrationID, pollToken string, ca *CertificateAuthority) (Registration, error) {
	var registration Registration
	var pollHash []byte
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT r.node_name, r.status, COALESCE(r.node_id, ''), r.poll_hash, r.expires_at, COALESCE(n.certificate_pem, X'')
		FROM registrations r LEFT JOIN nodes n ON n.id = r.node_id WHERE r.id = ?`, registrationID).
		Scan(&registration.NodeName, &registration.Status, &registration.NodeID, &pollHash, &expiresAt, &registration.Certificate)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, ErrNotFound
	}
	if err != nil {
		return Registration{}, fmt.Errorf("load registration status: %w", err)
	}
	if time.Now().UTC().Unix() > expiresAt || !constantTimeEqual(pollHash, security.TokenHash(pollToken)) {
		return Registration{}, ErrUnauthorized
	}
	registration.ID = registrationID
	if registration.Status == "approved" {
		registration.CAPEM = ca.CertificatePEM()
	}
	return registration, nil
}

func (s *Store) RotateNodeCertificate(ctx context.Context, nodeID string, csrPEM []byte, ca *CertificateAuthority) (Registration, error) {
	var nodeName, previousSerial string
	err := s.db.QueryRowContext(ctx, `SELECT name, certificate_serial FROM nodes WHERE id = ? AND revoked_at IS NULL`, nodeID).Scan(&nodeName, &previousSerial)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, ErrNotFound
	}
	if err != nil {
		return Registration{}, fmt.Errorf("load node: %w", err)
	}
	certificate, serial, err := ca.SignNodeCSR(csrPEM, nodeID, nodeName)
	if err != nil {
		return Registration{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registration{}, fmt.Errorf("start certificate rotation: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE certificates SET revoked_at = ? WHERE serial = ?`, nowUnix(), previousSerial)
	if err != nil {
		return Registration{}, fmt.Errorf("revoke prior certificate: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE nodes SET certificate_pem = ?, certificate_serial = ? WHERE id = ?`, certificate, serial, nodeID)
	if err != nil {
		return Registration{}, fmt.Errorf("replace node certificate: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO certificates (serial, node_id, certificate_pem, revoked_at, issued_at)
		VALUES (?, ?, ?, NULL, ?)`, serial, nodeID, certificate, nowUnix())
	if err != nil {
		return Registration{}, fmt.Errorf("record rotated certificate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Registration{}, fmt.Errorf("commit certificate rotation: %w", err)
	}
	return Registration{NodeID: nodeID, NodeName: nodeName, Status: "approved", Certificate: certificate, CAPEM: ca.CertificatePEM()}, nil
}

func (s *Store) RevokeNode(ctx context.Context, nodeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start node revocation: %w", err)
	}
	defer tx.Rollback()
	var serial string
	err = tx.QueryRowContext(ctx, `SELECT certificate_serial FROM nodes WHERE id = ? AND revoked_at IS NULL`, nodeID).Scan(&serial)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load node certificate: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE nodes SET revoked_at = ? WHERE id = ?`, nowUnix(), nodeID)
	if err != nil {
		return fmt.Errorf("revoke node: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE certificates SET revoked_at = ? WHERE serial = ?`, nowUnix(), serial)
	if err != nil {
		return fmt.Errorf("revoke node certificate: %w", err)
	}
	return tx.Commit()
}

func (s *Store) NodeForCertificate(ctx context.Context, serial string) (Node, error) {
	var node Node
	err := s.db.QueryRowContext(ctx, `SELECT n.id, n.name, n.agent_version, n.os, n.architecture, n.sing_box_version,
		COALESCE(n.capabilities, ''), n.last_seen_at
		FROM certificates c JOIN nodes n ON n.id = c.node_id
		WHERE c.serial = ? AND c.revoked_at IS NULL AND n.revoked_at IS NULL`, serial).
		Scan(&node.ID, &node.Name, &node.AgentVersion, &node.OS, &node.Architecture, &node.SingBox, &node.Capabilities, new(sql.NullInt64))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrUnauthorized
	}
	if err != nil {
		return Node{}, fmt.Errorf("load certificate node: %w", err)
	}
	return node, nil
}

func (s *Store) UpdateAgentStatus(ctx context.Context, nodeID string, status AgentStatus) error {
	if len(status.AgentVersion) > 128 || len(status.OS) > 128 || len(status.Architecture) > 128 || len(status.SingBox) > 128 || len(status.Capabilities) > 32*1024 || len(status.Metrics) > 256*1024 {
		return errors.New("agent status exceeds allowed size")
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE nodes SET agent_version = ?, os = ?, architecture = ?, sing_box_version = ?, capabilities = ?, last_seen_at = ?
		WHERE id = ? AND revoked_at IS NULL`, status.AgentVersion, status.OS, status.Architecture, status.SingBox, status.Capabilities, nowUnix(), nodeID)
	if err != nil {
		return fmt.Errorf("update agent status: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read agent status update: %w", err)
	}
	if changed != 1 {
		return ErrUnauthorized
	}
	if status.Metrics != "" {
		_, err := s.db.ExecContext(ctx, `INSERT INTO node_metrics (node_id, report, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(node_id) DO UPDATE SET report = excluded.report, updated_at = excluded.updated_at`, nodeID, status.Metrics, nowUnix())
		if err != nil {
			return fmt.Errorf("store node metrics: %w", err)
		}
	}
	return nil
}

func (s *Store) NodeMetrics(ctx context.Context, nodeID string) (json.RawMessage, string, error) {
	var report string
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT report, updated_at FROM node_metrics WHERE node_id = ?`, nodeID).Scan(&report, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("load node metrics: %w", err)
	}
	if !json.Valid([]byte(report)) {
		return nil, "", errors.New("stored metrics are invalid")
	}
	return json.RawMessage(report), time.Unix(updatedAt, 0).UTC().Format(time.RFC3339), nil
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, agent_version, os, architecture, sing_box_version,
		COALESCE(capabilities, ''), last_seen_at FROM nodes WHERE revoked_at IS NULL ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	threshold := time.Now().UTC().Add(-90 * time.Second).Unix()
	var nodes []Node
	for rows.Next() {
		var node Node
		var lastSeen sql.NullInt64
		if err := rows.Scan(&node.ID, &node.Name, &node.AgentVersion, &node.OS, &node.Architecture, &node.SingBox, &node.Capabilities, &lastSeen); err != nil {
			return nil, fmt.Errorf("read node: %w", err)
		}
		if lastSeen.Valid {
			node.Online = lastSeen.Int64 >= threshold
			node.LastSeenAt = time.Unix(lastSeen.Int64, 0).UTC().Format(time.RFC3339)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}
	return nodes, nil
}

func (s *Store) GetNode(ctx context.Context, nodeID string) (Node, error) {
	var node Node
	var lastSeen sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, agent_version, os, architecture, sing_box_version,
		COALESCE(capabilities, ''), last_seen_at FROM nodes WHERE id = ? AND revoked_at IS NULL`, nodeID).
		Scan(&node.ID, &node.Name, &node.AgentVersion, &node.OS, &node.Architecture, &node.SingBox, &node.Capabilities, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("load node: %w", err)
	}
	if lastSeen.Valid {
		node.Online = lastSeen.Int64 >= time.Now().UTC().Add(-90*time.Second).Unix()
		node.LastSeenAt = time.Unix(lastSeen.Int64, 0).UTC().Format(time.RFC3339)
	}
	return node, nil
}

func (s *Store) SetNodeSingBoxVersion(ctx context.Context, nodeID, version string) error {
	if version == "" || len(version) > 128 || strings.ContainsAny(version, "\r\n") {
		return errors.New("sing-box version is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE nodes SET sing_box_version = ? WHERE id = ? AND revoked_at IS NULL`, version, nodeID)
	if err != nil {
		return fmt.Errorf("update node sing-box version: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read node sing-box version update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListPendingRegistrations(ctx context.Context) ([]PendingRegistration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_name, capabilities, created_at FROM registrations WHERE status = 'pending' ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list pending registrations: %w", err)
	}
	defer rows.Close()
	var registrations []PendingRegistration
	for rows.Next() {
		var registration PendingRegistration
		var createdAt int64
		if err := rows.Scan(&registration.ID, &registration.NodeName, &registration.Capabilities, &createdAt); err != nil {
			return nil, fmt.Errorf("read pending registration: %w", err)
		}
		registration.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		registrations = append(registrations, registration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending registrations: %w", err)
	}
	return registrations, nil
}

func (s *Store) CreateTask(ctx context.Context, task Task) (Task, error) {
	if task.NodeID == "" || task.Kind == "" || task.IdempotencyKey == "" || task.ExpectedHash == "" {
		return Task{}, errors.New("task node, kind, idempotency key and expected hash are required")
	}
	if len(task.Payload) > 4*1024*1024 || len(task.ResultSummary) > 8*1024 {
		return Task{}, errors.New("task payload or result exceeds allowed size")
	}
	if task.Kind != "singbox.apply_config" && task.Kind != "singbox.install" && task.Kind != "singbox.upgrade" && task.Kind != "nginx.apply_config" && task.Kind != "firewall.apply" && task.Kind != "fail2ban.apply" {
		return Task{}, errors.New("unsupported task kind")
	}
	if task.OperatorID != "" {
		var operatorExists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM operators WHERE id = ? AND enabled = 1`, task.OperatorID).Scan(&operatorExists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Task{}, ErrUnauthorized
			}
			return Task{}, fmt.Errorf("check task operator: %w", err)
		}
	}
	var nodeExists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ? AND revoked_at IS NULL`, task.NodeID).Scan(&nodeExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		return Task{}, fmt.Errorf("check task node: %w", err)
	}
	var existing Task
	err := s.db.QueryRowContext(ctx, `SELECT id, node_id, COALESCE(created_by, ''), kind, idempotency_key, payload, expected_hash, status, COALESCE(result_summary, ''), created_at,
		COALESCE(started_at, 0), COALESCE(finished_at, 0) FROM tasks WHERE node_id = ? AND idempotency_key = ?`, task.NodeID, task.IdempotencyKey).
		Scan(&existing.ID, &existing.NodeID, &existing.OperatorID, &existing.Kind, &existing.IdempotencyKey, &existing.Payload, &existing.ExpectedHash, &existing.Status, &existing.ResultSummary,
			new(int64), new(int64), new(int64))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("lookup idempotent task: %w", err)
	}
	task.ID, err = newID()
	if err != nil {
		return Task{}, err
	}
	createdAt := nowUnix()
	task.Status = "queued"
	task.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `INSERT INTO tasks (id, node_id, created_by, kind, idempotency_key, payload, expected_hash, status, result_summary, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', NULL, ?, NULL, NULL)`, task.ID, task.NodeID, nullableString(task.OperatorID), task.Kind, task.IdempotencyKey, task.Payload, task.ExpectedHash, createdAt)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	return task, nil
}

func (s *Store) CreateListener(ctx context.Context, listener Listener) (Listener, error) {
	if listener.NodeID == "" || listener.Name == "" || len(listener.Name) > 128 {
		return Listener{}, errors.New("listener node and a name up to 128 characters are required")
	}
	if err := ValidateProtocolSpec(listener.Spec); err != nil {
		return Listener{}, err
	}
	if err := ValidateListenerAddress(listener.ListenAddr, listener.Port); err != nil {
		return Listener{}, err
	}
	if listener.BackendPort == 0 {
		listener.BackendPort = listener.Port
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ? AND revoked_at IS NULL`, listener.NodeID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Listener{}, ErrNotFound
		}
		return Listener{}, err
	}
	var conflict string
	if listener.Enabled {
		err := s.db.QueryRowContext(ctx, `SELECT id FROM listeners WHERE node_id = ? AND listen_address = ? AND network = ? AND port = ? AND enabled = 1`, listener.NodeID, listener.ListenAddr, listener.Spec.Network, listener.Port).Scan(&conflict)
		if err == nil {
			return Listener{}, ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Listener{}, fmt.Errorf("check listener port conflict: %w", err)
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM listeners WHERE node_id = ? AND name = ?`, listener.NodeID, listener.Name).Scan(&conflict); err == nil {
		return Listener{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Listener{}, fmt.Errorf("check listener name conflict: %w", err)
	}
	spec, err := json.Marshal(listener.Spec)
	if err != nil {
		return Listener{}, fmt.Errorf("encode listener spec: %w", err)
	}
	listener.ID, err = newID()
	if err != nil {
		return Listener{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO listeners (id, node_id, name, protocol, network, listen_address, port, backend_port, enabled, spec, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, listener.ID, listener.NodeID, listener.Name, listener.Spec.Protocol, listener.Spec.Network, listener.ListenAddr, listener.Port, listener.BackendPort, listener.Enabled, string(spec), nowUnix(), nowUnix())
	if err != nil {
		return Listener{}, fmt.Errorf("create listener: %w", err)
	}
	return listener, nil
}

func (s *Store) UpdateListener(ctx context.Context, listener Listener) (Listener, error) {
	if listener.ID == "" || listener.NodeID == "" || listener.Name == "" || len(listener.Name) > 128 {
		return Listener{}, errors.New("listener ID, node and a name up to 128 characters are required")
	}
	if err := ValidateProtocolSpec(listener.Spec); err != nil {
		return Listener{}, err
	}
	if err := ValidateListenerAddress(listener.ListenAddr, listener.Port); err != nil {
		return Listener{}, err
	}
	if listener.BackendPort == 0 {
		listener.BackendPort = listener.Port
	}
	var existingNode, existingProtocol string
	err := s.db.QueryRowContext(ctx, `SELECT node_id, protocol FROM listeners WHERE id = ?`, listener.ID).Scan(&existingNode, &existingProtocol)
	if errors.Is(err, sql.ErrNoRows) {
		return Listener{}, ErrNotFound
	}
	if err != nil {
		return Listener{}, fmt.Errorf("load listener: %w", err)
	}
	if existingNode != listener.NodeID {
		return Listener{}, ErrForbidden
	}
	if existingProtocol != listener.Spec.Protocol {
		var endpointCount int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoints WHERE listener_id = ?`, listener.ID).Scan(&endpointCount); err != nil {
			return Listener{}, fmt.Errorf("count listener endpoints: %w", err)
		}
		if endpointCount != 0 {
			return Listener{}, errors.New("delete the listener endpoints before changing its protocol")
		}
	}
	var conflict string
	if listener.Enabled {
		err = s.db.QueryRowContext(ctx, `SELECT id FROM listeners WHERE node_id = ? AND listen_address = ? AND network = ? AND port = ? AND enabled = 1 AND id <> ?`, listener.NodeID, listener.ListenAddr, listener.Spec.Network, listener.Port, listener.ID).Scan(&conflict)
		if err == nil {
			return Listener{}, ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Listener{}, fmt.Errorf("check listener port conflict: %w", err)
		}
	}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM listeners WHERE node_id = ? AND name = ? AND id <> ?`, listener.NodeID, listener.Name, listener.ID).Scan(&conflict)
	if err == nil {
		return Listener{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Listener{}, fmt.Errorf("check listener name conflict: %w", err)
	}
	spec, err := json.Marshal(listener.Spec)
	if err != nil {
		return Listener{}, fmt.Errorf("encode listener spec: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE listeners SET name = ?, protocol = ?, network = ?, listen_address = ?, port = ?, backend_port = ?, enabled = ?, spec = ?, updated_at = ? WHERE id = ?`,
		listener.Name, listener.Spec.Protocol, listener.Spec.Network, listener.ListenAddr, listener.Port, listener.BackendPort, listener.Enabled, string(spec), nowUnix(), listener.ID)
	if err != nil {
		return Listener{}, fmt.Errorf("update listener: %w", err)
	}
	return listener, nil
}

func (s *Store) ListListeners(ctx context.Context, nodeID string) ([]Listener, error) {
	query := `SELECT id, node_id, name, listen_address, port, backend_port, enabled, spec FROM listeners`
	args := []any{}
	if nodeID != "" {
		query += " WHERE node_id = ?"
		args = append(args, nodeID)
	}
	query += " ORDER BY node_id, name, id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list listeners: %w", err)
	}
	defer rows.Close()
	var listeners []Listener
	for rows.Next() {
		var listener Listener
		var spec string
		if err := rows.Scan(&listener.ID, &listener.NodeID, &listener.Name, &listener.ListenAddr, &listener.Port, &listener.BackendPort, &listener.Enabled, &spec); err != nil {
			return nil, fmt.Errorf("read listener: %w", err)
		}
		if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
			return nil, fmt.Errorf("decode listener spec: %w", err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, rows.Err()
}

func (s *Store) SetListenerEnabled(ctx context.Context, listenerID string, enabled bool) error {
	if enabled {
		var nodeID, address, network string
		var port uint16
		if err := s.db.QueryRowContext(ctx, `SELECT node_id, listen_address, network, port FROM listeners WHERE id = ?`, listenerID).Scan(&nodeID, &address, &network, &port); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var other string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM listeners WHERE node_id = ? AND listen_address = ? AND network = ? AND port = ? AND enabled = 1 AND id <> ?`, nodeID, address, network, port, listenerID).Scan(&other)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE listeners SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, nowUnix(), listenerID)
	if err != nil {
		return fmt.Errorf("set listener state: %w", err)
	}
	count, _ := updated.RowsAffected()
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteListener(ctx context.Context, listenerID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM ingress_routes WHERE listener_id = ?`, listenerID); err != nil {
		return fmt.Errorf("delete listener ingress routes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM endpoints WHERE listener_id = ?`, listenerID); err != nil {
		return fmt.Errorf("delete listener endpoints: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM listeners WHERE id = ?`, listenerID)
	if err != nil {
		return fmt.Errorf("delete listener: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) CreateEndpoint(ctx context.Context, endpoint Endpoint, credentials EndpointCredentials) (Endpoint, error) {
	if endpoint.ListenerID == "" || endpoint.Name == "" || len(endpoint.Name) > 128 {
		return Endpoint{}, errors.New("endpoint listener and a name up to 128 characters are required")
	}
	var spec string
	if err := s.db.QueryRowContext(ctx, `SELECT spec FROM listeners WHERE id = ?`, endpoint.ListenerID).Scan(&spec); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Endpoint{}, ErrNotFound
		}
		return Endpoint{}, err
	}
	var listenerSpec ProtocolSpec
	if err := json.Unmarshal([]byte(spec), &listenerSpec); err != nil {
		return Endpoint{}, fmt.Errorf("decode listener spec: %w", err)
	}
	if err := ValidateEndpointCredentials(listenerSpec.Protocol, credentials); err != nil {
		return Endpoint{}, err
	}
	var duplicate string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM endpoints WHERE listener_id = ? AND name = ?`, endpoint.ListenerID, endpoint.Name).Scan(&duplicate); err == nil {
		return Endpoint{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Endpoint{}, fmt.Errorf("check endpoint name conflict: %w", err)
	}
	plain, err := json.Marshal(credentials)
	if err != nil {
		return Endpoint{}, err
	}
	encrypted, err := security.Encrypt(s.masterKey, plain)
	if err != nil {
		return Endpoint{}, err
	}
	endpoint.ID, err = newID()
	if err != nil {
		return Endpoint{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO endpoints (id, listener_id, name, credentials, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, endpoint.ID, endpoint.ListenerID, endpoint.Name, encrypted, endpoint.Enabled, nowUnix(), nowUnix())
	if err != nil {
		return Endpoint{}, fmt.Errorf("create endpoint: %w", err)
	}
	return endpoint, nil
}

func (s *Store) UpdateEndpoint(ctx context.Context, endpoint Endpoint, credentials *EndpointCredentials) (Endpoint, error) {
	if endpoint.ID == "" || endpoint.ListenerID == "" || endpoint.Name == "" || len(endpoint.Name) > 128 {
		return Endpoint{}, errors.New("endpoint ID, listener and a name up to 128 characters are required")
	}
	var protocol, existingListener string
	err := s.db.QueryRowContext(ctx, `SELECT l.protocol, e.listener_id FROM endpoints e JOIN listeners l ON l.id = e.listener_id WHERE e.id = ?`, endpoint.ID).Scan(&protocol, &existingListener)
	if errors.Is(err, sql.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("load endpoint: %w", err)
	}
	if existingListener != endpoint.ListenerID {
		return Endpoint{}, ErrForbidden
	}
	var duplicate string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM endpoints WHERE listener_id = ? AND name = ? AND id <> ?`, endpoint.ListenerID, endpoint.Name, endpoint.ID).Scan(&duplicate)
	if err == nil {
		return Endpoint{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Endpoint{}, fmt.Errorf("check endpoint name conflict: %w", err)
	}
	if credentials == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE endpoints SET name = ?, enabled = ?, updated_at = ? WHERE id = ?`, endpoint.Name, endpoint.Enabled, nowUnix(), endpoint.ID)
	} else {
		if err := ValidateEndpointCredentials(protocol, *credentials); err != nil {
			return Endpoint{}, err
		}
		plain, err := json.Marshal(*credentials)
		if err != nil {
			return Endpoint{}, err
		}
		encrypted, err := security.Encrypt(s.masterKey, plain)
		if err != nil {
			return Endpoint{}, err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE endpoints SET name = ?, credentials = ?, enabled = ?, updated_at = ? WHERE id = ?`, endpoint.Name, encrypted, endpoint.Enabled, nowUnix(), endpoint.ID)
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("update endpoint: %w", err)
	}
	return endpoint, nil
}

func (s *Store) SetEndpointEnabled(ctx context.Context, endpointID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE endpoints SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, nowUnix(), endpointID)
	if err != nil {
		return fmt.Errorf("set endpoint state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read endpoint state update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteEndpoint(ctx context.Context, endpointID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM endpoints WHERE id = ?`, endpointID)
	if err != nil {
		return fmt.Errorf("delete endpoint: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read endpoint deletion: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListEndpoints(ctx context.Context, listenerID string) ([]Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, listener_id, name, enabled FROM endpoints WHERE listener_id = ? ORDER BY name, id`, listenerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var endpoints []Endpoint
	for rows.Next() {
		var endpoint Endpoint
		if err := rows.Scan(&endpoint.ID, &endpoint.ListenerID, &endpoint.Name, &endpoint.Enabled); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, rows.Err()
}

func (s *Store) CreateRouteRule(ctx context.Context, rule RouteRule) (RouteRule, error) {
	if err := ValidateRouteRule(rule); err != nil {
		return RouteRule{}, err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ? AND revoked_at IS NULL`, rule.NodeID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RouteRule{}, ErrNotFound
		}
		return RouteRule{}, err
	}
	domains, _ := json.Marshal(rule.Domains)
	suffixes, _ := json.Marshal(rule.DomainSuffix)
	cidrs, _ := json.Marshal(rule.CIDRs)
	id, err := newID()
	if err != nil {
		return RouteRule{}, err
	}
	rule.ID = id
	_, err = s.db.ExecContext(ctx, `INSERT INTO route_rules (id, node_id, priority, enabled, domains, domain_suffix, cidrs, port, network, protocol, inbound_tag, endpoint_name, action, outbound_tag, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rule.ID, rule.NodeID, rule.Priority, rule.Enabled, string(domains), string(suffixes), string(cidrs), rule.Port, rule.Network, rule.Protocol, rule.InboundTag, rule.EndpointName, rule.Action, rule.OutboundTag, nowUnix(), nowUnix())
	if err != nil {
		return RouteRule{}, fmt.Errorf("create route rule: %w", err)
	}
	return rule, nil
}

func (s *Store) ListRouteRules(ctx context.Context, nodeID string) ([]RouteRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, priority, enabled, domains, domain_suffix, cidrs, port, network, protocol, inbound_tag, endpoint_name, action, outbound_tag FROM route_rules WHERE node_id = ? ORDER BY priority, id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []RouteRule
	for rows.Next() {
		var rule RouteRule
		var domains, suffixes, cidrs string
		if err := rows.Scan(&rule.ID, &rule.NodeID, &rule.Priority, &rule.Enabled, &domains, &suffixes, &cidrs, &rule.Port, &rule.Network, &rule.Protocol, &rule.InboundTag, &rule.EndpointName, &rule.Action, &rule.OutboundTag); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(domains), &rule.Domains); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(suffixes), &rule.DomainSuffix); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cidrs), &rule.CIDRs); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) UpdateRouteRule(ctx context.Context, rule RouteRule) (RouteRule, error) {
	if rule.ID == "" {
		return RouteRule{}, errors.New("rule ID is required")
	}
	if err := ValidateRouteRule(rule); err != nil {
		return RouteRule{}, err
	}
	var currentNodeID string
	err := s.db.QueryRowContext(ctx, `SELECT node_id FROM route_rules WHERE id = ?`, rule.ID).Scan(&currentNodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteRule{}, ErrNotFound
	}
	if err != nil {
		return RouteRule{}, fmt.Errorf("load route rule: %w", err)
	}
	if currentNodeID != rule.NodeID {
		return RouteRule{}, ErrForbidden
	}
	domains, _ := json.Marshal(rule.Domains)
	suffixes, _ := json.Marshal(rule.DomainSuffix)
	cidrs, _ := json.Marshal(rule.CIDRs)
	_, err = s.db.ExecContext(ctx, `UPDATE route_rules SET priority = ?, enabled = ?, domains = ?, domain_suffix = ?, cidrs = ?, port = ?, network = ?, protocol = ?, inbound_tag = ?, endpoint_name = ?, action = ?, outbound_tag = ?, updated_at = ? WHERE id = ?`,
		rule.Priority, rule.Enabled, string(domains), string(suffixes), string(cidrs), rule.Port, rule.Network, rule.Protocol, rule.InboundTag, rule.EndpointName, rule.Action, rule.OutboundTag, nowUnix(), rule.ID)
	if err != nil {
		return RouteRule{}, fmt.Errorf("update route rule: %w", err)
	}
	return rule, nil
}

func (s *Store) SetRouteRuleEnabled(ctx context.Context, ruleID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE route_rules SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, nowUnix(), ruleID)
	if err != nil {
		return fmt.Errorf("set route rule state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read route rule state update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetRouteRulePriority(ctx context.Context, ruleID string, priority int) error {
	if priority < 0 || priority > 1_000_000 {
		return errors.New("rule priority is out of range")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE route_rules SET priority = ?, updated_at = ? WHERE id = ?`, priority, nowUnix(), ruleID)
	if err != nil {
		return fmt.Errorf("set route rule priority: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read route rule priority update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRouteRule(ctx context.Context, ruleID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM route_rules WHERE id = ?`, ruleID)
	if err != nil {
		return fmt.Errorf("delete route rule: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read route rule deletion: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) PendingTasks(ctx context.Context, nodeID string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, COALESCE(created_by, ''), kind, idempotency_key, payload, expected_hash, status, COALESCE(result_summary, ''), created_at,
		COALESCE(started_at, 0), COALESCE(finished_at, 0) FROM tasks WHERE node_id = ? AND status IN ('queued', 'dispatched') ORDER BY created_at, id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list pending tasks: %w", err)
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var task Task
		var createdAt, startedAt, finishedAt int64
		if err := rows.Scan(&task.ID, &task.NodeID, &task.OperatorID, &task.Kind, &task.IdempotencyKey, &task.Payload, &task.ExpectedHash, &task.Status, &task.ResultSummary, &createdAt, &startedAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("read pending task: %w", err)
		}
		task.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		if startedAt != 0 {
			task.StartedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		}
		if finishedAt != 0 {
			task.FinishedAt = time.Unix(finishedAt, 0).UTC().Format(time.RFC3339)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) ListTasks(ctx context.Context, nodeID, status string) ([]Task, error) {
	query := `SELECT id, node_id, COALESCE(created_by, ''), kind, idempotency_key, payload, expected_hash, status, COALESCE(result_summary, ''), created_at,
		COALESCE(started_at, 0), COALESCE(finished_at, 0) FROM tasks WHERE 1 = 1`
	arguments := []any{}
	if nodeID != "" {
		query += " AND node_id = ?"
		arguments = append(arguments, nodeID)
	}
	if status != "" {
		if status != "queued" && status != "dispatched" && status != "succeeded" && status != "failed" && status != "rolled_back" {
			return nil, errors.New("task status filter is invalid")
		}
		query += " AND status = ?"
		arguments = append(arguments, status)
	}
	query += " ORDER BY created_at DESC, id DESC"
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var task Task
		var createdAt, startedAt, finishedAt int64
		if err := rows.Scan(&task.ID, &task.NodeID, &task.OperatorID, &task.Kind, &task.IdempotencyKey, &task.Payload, &task.ExpectedHash, &task.Status, &task.ResultSummary, &createdAt, &startedAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("read task: %w", err)
		}
		task.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		if startedAt != 0 {
			task.StartedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		}
		if finishedAt != 0 {
			task.FinishedAt = time.Unix(finishedAt, 0).UTC().Format(time.RFC3339)
		}
		task.Payload = ""
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) TaskByID(ctx context.Context, taskID string) (Task, error) {
	var task Task
	var createdAt, startedAt, finishedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, node_id, COALESCE(created_by, ''), kind, idempotency_key, payload, expected_hash, status, COALESCE(result_summary, ''), created_at,
		COALESCE(started_at, 0), COALESCE(finished_at, 0) FROM tasks WHERE id = ?`, taskID).
		Scan(&task.ID, &task.NodeID, &task.OperatorID, &task.Kind, &task.IdempotencyKey, &task.Payload, &task.ExpectedHash, &task.Status, &task.ResultSummary, &createdAt, &startedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("load task: %w", err)
	}
	task.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	if startedAt != 0 {
		task.StartedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
	}
	if finishedAt != 0 {
		task.FinishedAt = time.Unix(finishedAt, 0).UTC().Format(time.RFC3339)
	}
	return task, nil
}

func (s *Store) MarkTaskDispatched(ctx context.Context, taskID, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = 'dispatched', started_at = COALESCE(started_at, ?) WHERE id = ? AND node_id = ? AND status = 'queued'`, nowUnix(), taskID, nodeID)
	if err != nil {
		return fmt.Errorf("mark task dispatched: %w", err)
	}
	return nil
}

func (s *Store) CompleteTask(ctx context.Context, taskID, nodeID, status, summary string) error {
	if status != "succeeded" && status != "failed" && status != "rolled_back" {
		return errors.New("invalid task completion status")
	}
	if len(summary) > 8*1024 {
		return errors.New("task summary exceeds allowed size")
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, result_summary = ?, finished_at = ? WHERE id = ? AND node_id = ? AND status IN ('queued', 'dispatched')`, status, summary, nowUnix(), taskID, nodeID)
	if err != nil {
		return fmt.Errorf("complete task: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read task completion: %w", err)
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}

// AppendAudit stores an intentionally redacted summary. Callers must never pass
// credentials, private keys, subscription values, or full configuration bodies.
func (s *Store) AppendAudit(ctx context.Context, operatorID, action, targetType, targetID, summary string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, operator_id, action, target_type, target_id, summary, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, operatorID, action, targetType, targetID, summary, nowUnix())
	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.operator_id, o.email, a.action, a.target_type, a.target_id, a.summary, a.created_at
		FROM audit_events a JOIN operators o ON o.id = a.operator_id ORDER BY a.created_at DESC, a.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var createdAt int64
		if err := rows.Scan(&event.ID, &event.OperatorID, &event.Email, &event.Action, &event.TargetType, &event.TargetID, &event.Summary, &createdAt); err != nil {
			return nil, fmt.Errorf("read audit event: %w", err)
		}
		event.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS operators (
  id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
  totp_secret BLOB NOT NULL, role TEXT NOT NULL, enabled INTEGER NOT NULL,
  created_at INTEGER NOT NULL, last_login_at INTEGER
);
CREATE TABLE IF NOT EXISTS login_challenges (
  id_hash BLOB PRIMARY KEY, operator_id TEXT NOT NULL REFERENCES operators(id),
  expires_at INTEGER NOT NULL, used_at INTEGER
);
CREATE TABLE IF NOT EXISTS sessions (
  id_hash BLOB PRIMARY KEY, operator_id TEXT NOT NULL REFERENCES operators(id), csrf_hash BLOB NOT NULL,
  expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS registration_tokens (
  id TEXT PRIMARY KEY, token_hash BLOB NOT NULL UNIQUE, created_by TEXT NOT NULL REFERENCES operators(id),
  expires_at INTEGER NOT NULL, used_at INTEGER, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS registrations (
  id TEXT PRIMARY KEY, poll_hash BLOB NOT NULL, node_name TEXT NOT NULL, csr_pem BLOB NOT NULL,
  capabilities TEXT NOT NULL, status TEXT NOT NULL, node_id TEXT, expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL, approved_at INTEGER
);
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, certificate_pem BLOB NOT NULL, certificate_serial TEXT NOT NULL UNIQUE,
  agent_version TEXT NOT NULL DEFAULT '', os TEXT NOT NULL DEFAULT '', architecture TEXT NOT NULL DEFAULT '',
  sing_box_version TEXT NOT NULL DEFAULT '', capabilities TEXT, last_seen_at INTEGER,
  revoked_at INTEGER, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS certificates (
  serial TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), certificate_pem BLOB NOT NULL,
  revoked_at INTEGER, issued_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY, operator_id TEXT NOT NULL REFERENCES operators(id), action TEXT NOT NULL,
  target_type TEXT NOT NULL, target_id TEXT NOT NULL, summary TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), created_by TEXT REFERENCES operators(id), kind TEXT NOT NULL, idempotency_key TEXT NOT NULL,
  payload TEXT NOT NULL, expected_hash TEXT NOT NULL, status TEXT NOT NULL, result_summary TEXT,
  created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER,
  UNIQUE(node_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS listeners (
  id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), name TEXT NOT NULL, protocol TEXT NOT NULL, network TEXT NOT NULL,
  listen_address TEXT NOT NULL, port INTEGER NOT NULL, backend_port INTEGER NOT NULL, enabled INTEGER NOT NULL, spec TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS endpoints (
  id TEXT PRIMARY KEY, listener_id TEXT NOT NULL REFERENCES listeners(id), name TEXT NOT NULL, credentials BLOB NOT NULL,
  enabled INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS route_rules (
  id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), priority INTEGER NOT NULL, enabled INTEGER NOT NULL,
  domains TEXT NOT NULL, domain_suffix TEXT NOT NULL, cidrs TEXT NOT NULL, port INTEGER NOT NULL, network TEXT NOT NULL,
  protocol TEXT NOT NULL, inbound_tag TEXT NOT NULL, endpoint_name TEXT NOT NULL, action TEXT NOT NULL, outbound_tag TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ingress_routes (
  id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), listener_id TEXT NOT NULL REFERENCES listeners(id),
  listen_address TEXT NOT NULL, port INTEGER NOT NULL, sni TEXT NOT NULL, enabled INTEGER NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS singbox_releases (
  id TEXT PRIMARY KEY, version TEXT NOT NULL, architecture TEXT NOT NULL, url TEXT NOT NULL, sha256 TEXT NOT NULL,
  enabled INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  UNIQUE(version, architecture)
);
CREATE TABLE IF NOT EXISTS managed_certificates (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, certificate_pem BLOB NOT NULL, private_key_pem BLOB NOT NULL,
  enabled INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS managed_reality_keys (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, public_key TEXT NOT NULL, private_key BLOB NOT NULL,
  enabled INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS subscriptions (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, node_id TEXT REFERENCES nodes(id), name TEXT NOT NULL,
  url_encrypted BLOB, format TEXT NOT NULL, endpoint_ids TEXT NOT NULL, token_hash BLOB UNIQUE,
  enabled INTEGER NOT NULL, last_status TEXT NOT NULL, last_error TEXT, last_processed_at INTEGER,
  generated_version INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS subscription_rules (
  id TEXT PRIMARY KEY, subscription_id TEXT NOT NULL REFERENCES subscriptions(id), rule_json TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS node_metrics (
  node_id TEXT PRIMARY KEY REFERENCES nodes(id), report TEXT NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS firewall_rules (
  id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), action TEXT NOT NULL, protocol TEXT NOT NULL,
  cidr TEXT NOT NULL, port INTEGER NOT NULL, expires_at INTEGER, enabled INTEGER NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_registrations_poll ON registrations(id, poll_hash);
CREATE INDEX IF NOT EXISTS idx_tasks_node_status ON tasks(node_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_listeners_port ON listeners(node_id, listen_address, network, port, enabled);
CREATE INDEX IF NOT EXISTS idx_endpoints_listener ON endpoints(listener_id, enabled);
CREATE INDEX IF NOT EXISTS idx_route_rules_node_priority ON route_rules(node_id, priority, id);
CREATE INDEX IF NOT EXISTS idx_ingress_routes_node_endpoint ON ingress_routes(node_id, listen_address, port, sni, enabled);
CREATE INDEX IF NOT EXISTS idx_singbox_releases_version_architecture ON singbox_releases(version, architecture, enabled);
CREATE INDEX IF NOT EXISTS idx_managed_certificates_name ON managed_certificates(name, enabled);
CREATE INDEX IF NOT EXISTS idx_managed_reality_keys_name ON managed_reality_keys(name, enabled);
CREATE INDEX IF NOT EXISTS idx_subscriptions_kind_enabled ON subscriptions(kind, enabled);
CREATE INDEX IF NOT EXISTS idx_subscription_rules_subscription ON subscription_rules(subscription_id);
CREATE INDEX IF NOT EXISTS idx_node_metrics_updated ON node_metrics(updated_at);
CREATE INDEX IF NOT EXISTS idx_firewall_rules_node ON firewall_rules(node_id, enabled, expires_at);
`)
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	for _, column := range []string{
		"agent_version TEXT NOT NULL DEFAULT ''", "os TEXT NOT NULL DEFAULT ''", "architecture TEXT NOT NULL DEFAULT ''",
		"sing_box_version TEXT NOT NULL DEFAULT ''", "capabilities TEXT", "last_seen_at INTEGER",
	} {
		if err := s.addNodeColumn(ctx, column); err != nil {
			return err
		}
	}
	if err := s.addOperatorColumn(ctx, "last_login_at INTEGER"); err != nil {
		return err
	}
	if err := s.addTaskColumn(ctx, "created_by TEXT REFERENCES operators(id)"); err != nil {
		return err
	}
	return nil
}

func (s *Store) addNodeColumn(ctx context.Context, definition string) error {
	_, err := s.db.ExecContext(ctx, "ALTER TABLE nodes ADD COLUMN "+definition)
	if err == nil || strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return fmt.Errorf("migrate nodes table: %w", err)
}

func (s *Store) addOperatorColumn(ctx context.Context, definition string) error {
	_, err := s.db.ExecContext(ctx, "ALTER TABLE operators ADD COLUMN "+definition)
	if err == nil || strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return fmt.Errorf("migrate operators table: %w", err)
}

func (s *Store) addTaskColumn(ctx context.Context, definition string) error {
	_, err := s.db.ExecContext(ctx, "ALTER TABLE tasks ADD COLUMN "+definition)
	if err == nil || strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return fmt.Errorf("migrate tasks table: %w", err)
}

func loadOrCreateMasterKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, masterKeyFile)
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("master key must contain exactly 32 bytes")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create master key: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(key); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
}

func newID() (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func nowUnix() int64 { return time.Now().UTC().Unix() }

func constantTimeEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
