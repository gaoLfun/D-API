package store

import (
	"testing"

	"github.com/gaoLfun/dapi/internal/core"
)

func TestBalanceProtectionState(t *testing.T) {
	zero, positive := 0.0, 1.0
	zeroBalance := core.Balance{Status: "ok", Available: &zero}
	positiveBalance := core.Balance{Status: "ok", Available: &positive}

	suspended, checks, transition := balanceProtectionState(true, false, 0, zeroBalance, false)
	if suspended || checks != 1 || transition != core.BalanceUnchanged {
		t.Fatalf("first zero = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
	suspended, checks, transition = balanceProtectionState(true, suspended, checks, zeroBalance, false)
	if !suspended || checks != 2 || transition != core.BalanceSuspended {
		t.Fatalf("second zero = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
	suspended, checks, transition = balanceProtectionState(true, suspended, checks, positiveBalance, false)
	if suspended || checks != 0 || transition != core.BalanceResumed {
		t.Fatalf("recovery = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
}

func TestBalanceProtectionFailureBreaksSequence(t *testing.T) {
	zero := 0.0
	suspended, checks, _ := balanceProtectionState(true, false, 0, core.Balance{Status: "ok", Available: &zero}, false)
	suspended, checks, transition := balanceProtectionState(true, suspended, checks, core.Balance{Status: "unavailable"}, false)
	if suspended || checks != 0 || transition != core.BalanceUnchanged {
		t.Fatalf("failed check = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
}

func TestBalanceProtectionManualAndDisabledBehavior(t *testing.T) {
	zero := 0.0
	suspended, checks, transition := balanceProtectionState(true, false, 0, core.Balance{Status: "ok", Available: &zero}, true)
	if !suspended || checks != 2 || transition != core.BalanceSuspended {
		t.Fatalf("manual zero = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
	suspended, checks, transition = balanceProtectionState(false, suspended, checks, core.Balance{Status: "unavailable"}, false)
	if suspended || checks != 0 || transition != core.BalanceResumed {
		t.Fatalf("disabled protection = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
}

func TestBalanceProtectionKeepsPausedOnUnavailableAndResumesUnlimited(t *testing.T) {
	suspended, checks, transition := balanceProtectionState(true, true, 2, core.Balance{Status: "unavailable"}, false)
	if !suspended || checks != 0 || transition != core.BalanceUnchanged {
		t.Fatalf("unavailable while paused = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
	suspended, checks, transition = balanceProtectionState(true, suspended, checks, core.Balance{Status: "ok", Unlimited: true}, false)
	if suspended || checks != 0 || transition != core.BalanceResumed {
		t.Fatalf("unlimited recovery = suspended:%t checks:%d transition:%q", suspended, checks, transition)
	}
}
