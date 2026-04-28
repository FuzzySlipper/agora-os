package agentsim_test

import (
	"os/exec"
	"testing"
)

func TestSmokeScript_Syntax(t *testing.T) {
	cmd := exec.Command("bash", "-n", "../../test/phase4/smoke.sh")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke.sh syntax check failed: %v\n%s", err, string(out))
	}
}
