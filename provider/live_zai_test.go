package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/provider"
)

func TestLiveZAIChinaCodingPlan(t *testing.T) {
	if os.Getenv("OAP_LIVE_ZAI") != "1" {
		t.Skip("set OAP_LIVE_ZAI=1 for explicit China Coding Plan evidence")
	}
	credential := os.Getenv("OAP_ZAI_CODING_PLAN_KEY")
	if credential == "" {
		t.Fatal("OAP_ZAI_CODING_PLAN_KEY is required when OAP_LIVE_ZAI=1")
	}
	if os.Getenv("OAP_ZAI_CN_AUTHORIZED") != "1" {
		t.Fatal("set OAP_ZAI_CN_AUTHORIZED=1 only after verifying this key is authorized for open.bigmodel.cn China Coding Plan")
	}
	ids := []string{"zai-cn-responses-control", "zai-cn-responses-glm-5.3-flash"}
	if os.Getenv("OAP_LIVE_ZAI_ALL_WIRES") == "1" {
		ids = append(ids, "zai-cn-anthropic", "zai-cn-chat")
	}
	for _, id := range ids {
		preset, err := provider.ZAIPreset(id)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			result, err := provider.RunEvidence(ctx, nil, preset, credential)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Println(string(encoded))
		})
	}
}
