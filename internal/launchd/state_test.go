package launchd

import (
	"strings"
	"testing"
)

func TestJobStateIgnoresNestedDictionaries(t *testing.T) {
	for _, tc := range []struct {
		name, state, exit string
		active, exited    bool
		code              int
	}{
		{"completed", "not running", "last exit code = 0", false, true, 0},
		{"running", "running", "last exit code = (never exited)", true, false, 0},
		{"annotated exit", "not running", "last exit code = 78: EX_CONFIG", false, true, 78},
		{"signal", "exited", "last terminating signal = Terminated: 15", false, true, 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dump := "user/501/example = {\n\tstate = " + tc.state + "\n\t" + tc.exit + "\n\tresource coalition = {\n\t\tstate = active\n\t\tlast exit code = 99\n\t}\n\tenvironment = {\n\t\tstate = running\n\t}\n}\n"
			for _, spaces := range []bool{false, true} {
				input := dump
				if spaces {
					input = strings.ReplaceAll(input, "\t", "  ")
				}
				got, err := parseState(input)
				if err != nil || !got.Exists || got.Active != tc.active || got.Exited != tc.exited || got.ExitCode != tc.code {
					t.Fatal(got, err)
				}
			}
		})
	}
}

func TestMalformedNativeStatusFailsExplicitly(t *testing.T) {
	for _, dump := range []string{"", "job = {\n\tpid = 1\n}\n", "job = {\n\tstate = exited\n\tlast exit code = nope\n}\n", "job = {\n\tstate = exited\n\tlast terminating signal = nope\n}\n", "job = {\n\tstate = exited\n\tlast terminating signal = Terminated: nope\n}\n"} {
		if _, err := parseState(dump); err == nil {
			t.Fatalf("accepted malformed native status: %s", dump)
		}
	}
}
