package sdk

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const envFakeHost = "OAP_SDK_GO_FAKE_HOST"

const (
	scenarioOAP            = "oap-combined"
	scenarioSilent         = "silent"
	scenarioBadVersion     = "bad-version"
	scenarioHandshakeError = "handshake-error"
	scenarioExitAfterReady = "exit-after-ready"
	scenarioExitMidRequest = "exit-mid-request"
	scenarioGarbage        = "garbage"
	scenarioIgnoreStdin    = "ignore-stdin"
)

func fakeHostEnv(scenario string, knobs ...string) []string {
	env := append(os.Environ(), envFakeHost+"="+scenario)
	return append(env, knobs...)
}

var fakeStdoutMu sync.Mutex

func fakeEmit(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	fakeStdoutMu.Lock()
	defer fakeStdoutMu.Unlock()
	os.Stdout.Write(append(encoded, '\n'))
}

func fakeEmitRaw(line string) {
	fakeStdoutMu.Lock()
	defer fakeStdoutMu.Unlock()
	os.Stdout.WriteString(line + "\n")
}

func blockForever() {
	for {
		time.Sleep(time.Hour)
	}
}

func runFakeHost(scenario string) {
	if scenario == scenarioGarbage {
		fakeEmitRaw("this is not json")
		fakeEmitRaw("{ broken")
	}
	runOAPHost(scenario)
	os.Exit(0)
}
