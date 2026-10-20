// Package connections keeps saved-connections.json: one entry per connection airlock knows, a
// direct database (its definition and the choices remembered at its last opening) or a target
// reached through a jumphost (the choices remembered at its last connection). Passwords are never
// part of it.
package connections

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/environment"
)

// The AWS types of the targets reached through a jumphost.
const (
	TypeRDS         = "rds"
	TypeEKS         = "eks"
	TypeElastiCache = "elasticache"
	// TypeForward is a tunnel to any host:port (airlock forward), its target.
	TypeForward = "forward"
)

// JumphostTarget is a target reached through a jumphost: where it is, and the AWS profile of its
// last connection. The account, not the profile, identifies it: several profiles reach one account
// and profile names are local to each machine.
type JumphostTarget struct {
	Account string
	Region  string
	Type    string
	Target  string
	Profile string
}

// Key is the key of the target's entry: <account>/<region>/<type>/<target>.
func (t *JumphostTarget) Key() string {
	return JumphostKey(t.Account, t.Region, t.Type, t.Target)
}

// validate returns why t cannot key an entry; nil when it can.
func (t *JumphostTarget) validate() error {
	switch t.Type {
	case TypeRDS, TypeEKS, TypeElastiCache, TypeForward:
	default:
		return errors.New("type: must be rds, eks, elasticache or forward")
	}
	for _, field := range []struct{ name, value string }{{"target", t.Target}, {"account", t.Account}, {"region", t.Region}} {
		if field.value == "" || strings.Contains(field.value, "/") {
			return fmt.Errorf("%s: must not be empty nor contain /", field.name)
		}
	}
	return nil
}

// trimmedTarget is t with every field trimmed.
func trimmedTarget(t *JumphostTarget) JumphostTarget {
	return JumphostTarget{
		Account: strings.TrimSpace(t.Account), Region: strings.TrimSpace(t.Region), Type: strings.TrimSpace(t.Type),
		Target: strings.TrimSpace(t.Target), Profile: strings.TrimSpace(t.Profile),
	}
}

// JumphostKey is the key of the entry of a target reached through a jumphost.
func JumphostKey(account, region, resourceType, target string) string {
	return strings.Join([]string{
		strings.TrimSpace(account), strings.TrimSpace(region), strings.TrimSpace(resourceType), strings.TrimSpace(target),
	}, "/")
}

// Choices are what airlock remembers of the last connection of an entry; the zero value is none.
type Choices struct {
	// UsedAt is the last connection; zero for a direct database never opened.
	UsedAt time.Time
	// User is the database or cache user of a jumphost entry; a direct entry's is its definition's.
	User string
	// SignIn is the sign-in method (iam, password, secret or auto); empty reads as iam.
	SignIn string
	// SecretARN is the secret of the secret sign-in (jumphost entries only).
	SecretARN string
	// LocalPort is the last local port (jumphost entries only).
	LocalPort int
	// PasswordInKeychain reports that the password of the user is in the OS keychain.
	PasswordInKeychain bool
	MCPAllowWrites     bool
}

// Entry is one connection of the file: Direct or Jumphost is set, or Invalid tells why the entry
// could not be read. The file keeps an invalid entry as it was written.
type Entry struct {
	Direct   *directdb.Database
	Jumphost *JumphostTarget
	Invalid  error
	Key      string
	// raw is the JSON the entry was read from, written back as it is while the entry is invalid.
	raw     json.RawMessage
	Choices Choices
}

// ShortName is how the CLI names the entry: <type>/<target> for a jumphost entry, else its key.
func (e *Entry) ShortName() string {
	if e.Jumphost != nil {
		return e.Jumphost.Type + "/" + e.Jumphost.Target
	}
	return e.Key
}

// record is the JSON of one entry.
//
//nolint:govet // fieldalignment: the field order is the file's key order.
type record struct {
	Engine      directdb.Engine  `json:"engine,omitempty"`
	Host        string           `json:"host,omitempty"`
	Port        int              `json:"port,omitempty"`
	Database    string           `json:"database,omitempty"`
	TLS         directdb.TLSMode `json:"tls,omitempty"`
	Environment string           `json:"environment,omitempty"`
	// Production is the flag of an earlier airlock, read as the prod environment, never written.
	Production         bool            `json:"production,omitempty"`
	Type               string          `json:"type,omitempty"`
	Target             string          `json:"target,omitempty"`
	Jumphost           *jumphostRecord `json:"jumphost,omitempty"`
	User               string          `json:"user,omitempty"`
	SecretARN          string          `json:"secret_arn,omitempty"`
	SignIn             string          `json:"sign_in,omitempty"`
	PasswordInKeychain bool            `json:"password_in_keychain,omitempty"`
	LocalPort          int             `json:"local_port,omitempty"`
	UsedAt             *time.Time      `json:"used_at,omitempty"`
	MCPAllowWrites     bool            `json:"mcp_allow_writes"`
}

// jumphostRecord is the "jumphost" object of an entry reached through a jumphost.
//
//nolint:govet // fieldalignment: the field order is the file's key order.
type jumphostRecord struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Profile string `json:"profile,omitempty"`
}

// decodeEntry reads the entry key of the file; one it cannot read comes back with Invalid set.
func decodeEntry(key string, raw json.RawMessage) Entry {
	entry := Entry{Key: key, raw: raw}
	var rec record
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&rec)
	if err == nil && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		err = errors.New("must be an object")
	}
	if err == nil {
		if rec.Jumphost == nil {
			entry.Direct, err = directOf(key, &rec)
		} else {
			entry.Jumphost, err = jumphostOf(key, &rec)
		}
	}
	if err != nil {
		entry.Direct, entry.Jumphost = nil, nil
		entry.Invalid = fmt.Errorf("connection %q: %w", key, err)
		return entry
	}
	entry.Choices = Choices{
		SignIn: rec.SignIn, PasswordInKeychain: rec.PasswordInKeychain, MCPAllowWrites: rec.MCPAllowWrites,
	}
	if rec.UsedAt != nil {
		entry.Choices.UsedAt = rec.UsedAt.UTC()
	}
	if entry.Jumphost != nil {
		entry.Choices.User, entry.Choices.SecretARN, entry.Choices.LocalPort = rec.User, rec.SecretARN, rec.LocalPort
	}
	return entry
}

// directOf is the definition a record without jumphost holds.
func directOf(key string, rec *record) (*directdb.Database, error) {
	if rec.Type != "" || rec.Target != "" || rec.SecretARN != "" || rec.LocalPort != 0 {
		return nil, errors.New("type, target, secret_arn and local_port need a jumphost")
	}
	db := directdb.Database{
		Name: key, Engine: rec.Engine, Host: rec.Host, Port: rec.Port, Database: rec.Database,
		User: rec.User, TLS: rec.TLS, Environment: strings.ToLower(strings.TrimSpace(rec.Environment)),
	}
	if db.Environment == "" && rec.Production {
		db.Environment = environment.ProductionAlias
	}
	if err := db.Validate(); err != nil {
		return nil, err
	}
	return &db, nil
}

// jumphostOf is the target a record with jumphost holds; key must be its key.
func jumphostOf(key string, rec *record) (*JumphostTarget, error) {
	if rec.Engine != "" || rec.Host != "" || rec.Port != 0 || rec.Database != "" || rec.TLS != "" || rec.Environment != "" || rec.Production {
		return nil, errors.New("engine, host, port, database, tls and environment are for direct databases, without jumphost")
	}
	target := &JumphostTarget{
		Account: rec.Jumphost.Account, Region: rec.Jumphost.Region, Type: rec.Type, Target: rec.Target, Profile: rec.Jumphost.Profile,
	}
	if err := target.validate(); err != nil {
		return nil, err
	}
	if key != target.Key() {
		return nil, fmt.Errorf("the key must be %s", target.Key())
	}
	return target, nil
}

// encode returns the JSON of e: as it was read while e is invalid, else built from its fields.
func (e *Entry) encode() (json.RawMessage, error) {
	if e.Invalid != nil {
		return e.raw, nil
	}
	choices := e.Choices
	rec := record{SignIn: choices.SignIn, PasswordInKeychain: choices.PasswordInKeychain, MCPAllowWrites: choices.MCPAllowWrites}
	if !choices.UsedAt.IsZero() {
		rec.UsedAt = new(choices.UsedAt.UTC())
	}
	switch {
	case e.Direct != nil:
		db := e.Direct
		rec.Engine, rec.Host, rec.Port, rec.Database = db.Engine, db.Host, db.Port, db.Database
		rec.User, rec.TLS, rec.Environment = db.User, db.TLS, db.Environment
	case e.Jumphost != nil:
		target := e.Jumphost
		rec.Type, rec.Target = target.Type, target.Target
		rec.Jumphost = &jumphostRecord{Account: target.Account, Region: target.Region, Profile: target.Profile}
		rec.User, rec.SecretARN, rec.LocalPort = choices.User, choices.SecretARN, choices.LocalPort
	default:
		return nil, fmt.Errorf("connection %q is neither a direct database nor a target reached through a jumphost", e.Key)
	}
	data, err := json.Marshal(&rec)
	if err != nil {
		return nil, fmt.Errorf("failed to encode connection %q: %w", e.Key, err)
	}
	return data, nil
}

// DirectUser is the user the keychain password of a direct entry is remembered for, and whether the
// entry reports that password in the keychain. An invalid entry without jumphost is read leniently
// from its JSON, so that the password it may have remembered can still be forgotten; "" when the
// entry is a target reached through a jumphost or names no readable user.
func (e *Entry) DirectUser() (user string, passwordInKeychain bool) {
	switch {
	case e.Direct != nil:
		return e.Direct.User, e.Choices.PasswordInKeychain
	case e.Invalid == nil:
		return "", false
	}
	var lenient struct {
		Jumphost           json.RawMessage `json:"jumphost"`
		User               json.RawMessage `json:"user"`
		PasswordInKeychain json.RawMessage `json:"password_in_keychain"`
	}
	if err := json.Unmarshal(e.raw, &lenient); err != nil || (len(lenient.Jumphost) > 0 && !isJSONNull(lenient.Jumphost)) {
		return "", false
	}
	if err := json.Unmarshal(lenient.User, &user); err != nil {
		return "", false
	}
	if user = strings.TrimSpace(user); user == "" {
		return "", false
	}
	if err := json.Unmarshal(lenient.PasswordInKeychain, &passwordInKeychain); err != nil {
		passwordInKeychain = false
	}
	return user, passwordInKeychain
}

// isJSONNull reports whether raw is the JSON null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
