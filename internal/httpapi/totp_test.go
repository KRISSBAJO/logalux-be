package httpapi

import (
	"testing"
	"time"
)

// Test values from RFC 6238, appendix B (SHA-1), cut to six digits.
func TestTOTPMatchesTheRFC(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		got, err := totpCode(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Fatalf("at %d: got %s, want %s (err %v)", unix, got, want, err)
		}
	}
}

func TestTOTPAllowsOneStepOfClockDrift(t *testing.T) {
	secret, _ := newTOTPSecret()
	now := time.Unix(1700000000, 0)
	code, _ := totpCode(secret, now)
	if !totpValid(secret, code, now.Add(25*time.Second)) {
		t.Fatal("a code from the last step should pass")
	}
	if totpValid(secret, code, now.Add(2*time.Minute)) {
		t.Fatal("an old code should fail")
	}
	if totpValid(secret, "12345", now) || totpValid(secret, "", now) {
		t.Fatal("a short code should fail")
	}
}

func TestRandomCodeShape(t *testing.T) {
	c, err := randomCode(3, 4)
	if err != nil || len(c) != 14 || c[4] != '-' || c[9] != '-' {
		t.Fatalf("got %q (err %v)", c, err)
	}
}
