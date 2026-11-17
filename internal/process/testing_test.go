package process

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if handled, code := DispatchLaunch(os.Args); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
