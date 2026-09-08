package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"time"
)

type Map = map[string]any

// RoleUnselected is the audit role recorded for a real user who has not yet
// selected which of their several roles they are working under.
const RoleUnselected = "unselected"

// Principal is the authenticated caller. Roles is every role the account holds
// and is never changed by role selection. SelectedRole is the role the caller
// explicitly chose for this session, and it is the only role that authorizes an
// action: permissions and work views follow the selected role, never the union
// of held roles.
type Principal struct {
	ID           string
	SessionID    string
	Roles        []string
	SelectedRole string
}

// ActiveRole returns the single role the caller is currently working under.
// An account holding exactly one role works under it without an explicit
// selection; an account holding several has no active role until it selects one.
// A stale selection for a role the account no longer holds is ignored.
func (p Principal) ActiveRole() string {
	if p.SelectedRole != "" && Contains(p.Roles, p.SelectedRole) {
		return p.SelectedRole
	}
	if len(p.Roles) == 1 {
		return p.Roles[0]
	}
	return ""
}

// Has authorizes against the selected role only. An administrator working as a
// requester is a requester and nothing more: there is no blanket bypass.
func (p Principal) Has(roles ...string) bool {
	active := p.ActiveRole()
	if active == "" {
		return false
	}
	return Contains(roles, active)
}

// Holds reports whether the account carries the role at all, regardless of what
// it is currently working under. It answers membership questions, never
// authorization questions.
func (p Principal) Holds(roles ...string) bool {
	for _, have := range p.Roles {
		if Contains(roles, have) {
			return true
		}
	}
	return false
}

// RoleSelectionRequired reports a multi-role account that has not chosen the
// role it is acting under, so no role-scoped action may proceed yet.
func (p Principal) RoleSelectionRequired() bool {
	return p.ActiveRole() == "" && len(p.Roles) > 1
}

// AuditRole is the role recorded against this caller's actions. It is always
// populated so every audit record carries an outcome-bearing role.
func (p Principal) AuditRole() string {
	if active := p.ActiveRole(); active != "" {
		return active
	}
	return RoleUnselected
}

type HTTPError struct {
	Status int
	Code   string
	Err    error
}

func (e *HTTPError) Error() string { return e.Err.Error() }
func Fail(status int, code, message string) error {
	return &HTTPError{Status: status, Code: code, Err: errors.New(message)}
}

func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}

func String(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func Bool(v any) bool { n, _ := v.(bool); return n }
func Float(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
func Int(v any) int { return int(Float(v)) }
func Object(v any) Map {
	m, _ := v.(map[string]any)
	return m
}
func Slice(v any) []any {
	x, _ := v.([]any)
	return x
}
func Strings(v any) []string {
	if values, ok := v.([]string); ok {
		return append([]string{}, values...)
	}
	raw := Slice(v)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, String(item))
	}
	return out
}
func Time(v any) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, String(v))
	if err != nil {
		return time.Time{}, Fail(400, "invalid_time", "timestamps must use RFC3339")
	}
	return t.UTC(), nil
}

// Now returns the production clock unless an explicit application clock is
// supplied by a deterministic test environment.
func Now() time.Time {
	if value := os.Getenv("APPLICATION_FIXED_CLOCK"); value != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func Contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func DistanceKM(a, b Map) float64 {
	lat1 := Float(a["latitude"]) * math.Pi / 180
	lat2 := Float(b["latitude"]) * math.Pi / 180
	dLat := lat2 - lat1
	dLon := (Float(b["longitude"]) - Float(a["longitude"])) * math.Pi / 180
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 6371.0088 * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}
