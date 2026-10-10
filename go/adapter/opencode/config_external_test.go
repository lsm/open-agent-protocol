package opencode_test

import (
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/opencode"
)

func TestAHostOutsideThePackageCanNameAModelForTheConfig(t *testing.T) {
	model := &opencode.ModelRef{ProviderID: "zai", ID: "glm-5.3"}
	if _, err := opencode.New(opencode.Config{Endpoint: "http://127.0.0.1:1", Model: model}); err != nil {
		t.Fatal(err)
	}
}
