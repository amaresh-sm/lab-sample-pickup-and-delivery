package service

import (
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func TestStructuredCertificationIsAuthoritativeOverLegacyQualification(t *testing.T) {
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	expiredAt := at.Add(-time.Minute)
	user := &mongostore.User{
		Qualifications: []string{"LX-A"},
		Certifications: []mongostore.Certification{
			{Code: "LX-A", Active: true, ExpiresAt: &expiredAt},
		},
	}
	if userHasActiveCertificationAt(user, "LX-A", at) {
		t.Fatal("expired structured certification was bypassed by a legacy qualification")
	}
	user.Certifications[0].ExpiresAt = nil
	user.Certifications[0].Active = false
	if userHasActiveCertificationAt(user, "LX-A", at) {
		t.Fatal("inactive structured certification was bypassed by a legacy qualification")
	}
	user.Certifications[0].Active = true
	if !userHasActiveCertificationAt(user, "lx-a", at) {
		t.Fatal("active non-expiring structured certification was rejected")
	}
}

func TestLegacyQualificationRemainsEligibleWithoutStructuredRecord(t *testing.T) {
	user := &mongostore.User{Qualifications: []string{"LX-I"}}
	if !userHasActiveCertificationAt(user, "lx-i", time.Now()) {
		t.Fatal("legacy qualification should remain active for backward compatibility")
	}
}

func TestParseUserCertificationsValidatesAndNormalizesRecords(t *testing.T) {
	certifications, present, err := parseUserCertifications(core.Map{
		"certifications": []any{
			core.Map{"code": " LX-F ", "active": true, "expiresAt": "2026-08-01T00:00:00Z"},
		},
	})
	if err != nil || !present || len(certifications) != 1 {
		t.Fatalf("parse result = (%v, %v, %v)", certifications, present, err)
	}
	if certifications[0].Code != "LX-F" ||
		!certifications[0].Active ||
		certifications[0].ExpiresAt == nil {
		t.Fatalf("certification = %+v", certifications[0])
	}
	if _, _, err := parseUserCertifications(core.Map{
		"certifications": []any{
			core.Map{"code": "LX-F", "active": true},
			core.Map{"code": "lx-f", "active": false},
		},
	}); err == nil {
		t.Fatal("duplicate certification codes were accepted")
	}
}

func TestFairWorkerRejectsExpiredStructuredCertification(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-27T09:00:00Z")
	expiredAt := time.Date(2026, 7, 27, 8, 59, 0, 0, time.UTC)
	state := mongostore.NewState()
	state.Users["expired"] = &mongostore.User{
		ID: "expired", Roles: []string{"collection_professional"},
		Qualifications: []string{"LX-A"},
		Certifications: []mongostore.Certification{
			{Code: "LX-A", Active: true, ExpiresAt: &expiredAt},
		},
		Active: true, Available: true, CapacityUnits: 2,
	}
	state.Users["legacy-active"] = &mongostore.User{
		ID: "legacy-active", Roles: []string{"collection_professional"},
		Qualifications: []string{"LX-A"},
		Active:         true, Available: true, CapacityUnits: 2,
	}
	if got := fairWorker(state, "collection_professional", "LX-A", true, nil, ""); got != "legacy-active" {
		t.Fatalf("selected %q, want worker with active certification", got)
	}
}
