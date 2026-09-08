package service

import (
	"strings"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func parseUserCertifications(in core.Map) ([]mongostore.Certification, bool, error) {
	raw, present := in["certifications"]
	if !present {
		return nil, false, nil
	}
	values := core.Slice(raw)
	if typed, ok := raw.([]core.Map); ok {
		values = make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, item)
		}
	} else if raw != nil && values == nil {
		return nil, true, core.Fail(400, "invalid_certification", "certifications must be an array")
	}
	certifications := []mongostore.Certification{}
	seen := map[string]bool{}
	for _, value := range values {
		item := core.Object(value)
		if item == nil {
			return nil, true, core.Fail(400, "invalid_certification", "each certification must be an object")
		}
		code := strings.TrimSpace(core.String(item["code"]))
		if code == "" {
			return nil, true, core.Fail(400, "invalid_certification", "certification code is required")
		}
		if _, ok := item["active"]; !ok {
			return nil, true, core.Fail(400, "invalid_certification", "certification active state is required")
		}
		key := strings.ToLower(code)
		if seen[key] {
			return nil, true, core.Fail(400, "invalid_certification", "certification codes must be unique")
		}
		seen[key] = true
		certification := mongostore.Certification{Code: code, Active: core.Bool(item["active"])}
		if expiresAt := core.String(item["expiresAt"]); expiresAt != "" {
			parsed, err := core.Time(expiresAt)
			if err != nil {
				return nil, true, err
			}
			certification.ExpiresAt = pointerTime(parsed)
		}
		certifications = append(certifications, certification)
	}
	return certifications, true, nil
}

func certificationMaps(certifications []mongostore.Certification) []core.Map {
	out := make([]core.Map, 0, len(certifications))
	for _, certification := range certifications {
		item := core.Map{"code": certification.Code, "active": certification.Active}
		if certification.ExpiresAt != nil {
			item["expiresAt"] = formatTime(*certification.ExpiresAt)
		}
		out = append(out, item)
	}
	return out
}

// userHasActiveCertificationAt supports the legacy qualification strings while
// making a structured record authoritative whenever one exists for the code.
// This prevents an inactive or expired record from being bypassed by retaining
// the same legacy string.
func userHasActiveCertificationAt(user *mongostore.User, code string, at time.Time) bool {
	if user == nil || strings.TrimSpace(code) == "" {
		return false
	}
	code = strings.TrimSpace(code)
	foundStructured := false
	for _, certification := range user.Certifications {
		if !strings.EqualFold(strings.TrimSpace(certification.Code), code) {
			continue
		}
		foundStructured = true
		if certification.Active &&
			(certification.ExpiresAt == nil || certification.ExpiresAt.After(at)) {
			return true
		}
	}
	if foundStructured {
		return false
	}
	for _, qualification := range user.Qualifications {
		if strings.EqualFold(strings.TrimSpace(qualification), code) {
			return true
		}
	}
	return false
}

func userHasActiveCertification(user *mongostore.User, code string) bool {
	return userHasActiveCertificationAt(user, code, core.Now())
}
