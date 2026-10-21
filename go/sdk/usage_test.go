package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func observedUsage(client *Client, agent, stream bool) (*Usage, error) {
	ctx := context.Background()
	messages := []Message{UserMessage("usage")}
	if agent {
		request := AgentRequest{ModelRef: "fixture/other:test@ok", Messages: messages}
		if !stream {
			response, err := client.Agent.Run(ctx, request)
			if err != nil {
				return nil, err
			}
			return response.Usage, nil
		}
		events, err := client.Agent.Stream(ctx, request)
		if err != nil {
			return nil, err
		}
		defer events.Close()
		var usage *Usage
		for events.Next() {
			if terminal, ok := events.Event().(*AgentEnd); ok {
				usage = terminal.Usage
			}
		}
		return usage, events.Err()
	}
	request := CompletionRequest{ModelRef: "fixture/other:test@ok", Messages: messages}
	if !stream {
		response, err := client.Provider.Complete(ctx, request)
		if err != nil {
			return nil, err
		}
		return response.Usage, nil
	}
	events, err := client.Provider.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	defer events.Close()
	var usage *Usage
	for events.Next() {
		if terminal, ok := events.Event().(*MessageEnd); ok {
			usage = terminal.Usage
		}
	}
	return usage, events.Err()
}

func usageClient(t *testing.T, raw string) *Client {
	t.Helper()
	client, err := newTestClientWithOptions(t, &Options{BinaryPath: os.Args[0],
		Env: []string{envFakeHost + "=" + scenarioOAP, "OAP_SDK_GO_FAKE_USAGE=" + raw, "GORACE=atexit_sleep_ms=0"}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func usageJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestUsagePreservesAbsentZeroPartialAndMaximumMeasurements(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"absent", "null"},
		{`{}`, `{}`},
		{`{"input_tokens":0}`, `{"input":0}`},
		{`{"output_tokens":0}`, `{"output":0}`},
		{`{"total_tokens":18446744073709551615}`, `{"total":18446744073709551615}`},
		{`{"input_tokens":9007199254740993,"output_tokens":0}`, `{"input":9007199254740993,"output":0}`},
		{`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_write_input_tokens":0,"reasoning_tokens":0,"total_tokens":0}`, `{"input":0,"output":0,"cache_read":0,"cache_write":0,"reasoning":0,"total":0}`},
	}
	for i, test := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			client := usageClient(t, test.raw)
			for _, agent := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					got, err := observedUsage(client, agent, stream)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(got)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(usageJSON(t, raw), usageJSON(t, []byte(test.want))) {
						t.Fatalf("agent=%v stream=%v usage=%s, want %s", agent, stream, raw, test.want)
					}
				}
			}
		})
	}
}

func TestUsageRefusesMalformedNegativeAndOutOfRangeMeasurements(t *testing.T) {
	cases := []string{"null", "[]", "true", `"usage"`}
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "reasoning_tokens"} {
		for _, count := range []string{"null", "-1", "1.5", "true", `"3"`, "[]", "{}", "18446744073709551616"} {
			cases = append(cases, fmt.Sprintf(`{"%s":%s}`, field, count))
		}
	}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			client := usageClient(t, raw)
			for _, agent := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					_, err := observedUsage(client, agent, stream)
					var protocolError *ProtocolError
					if !errors.As(err, &protocolError) || protocolError.Code != CodeMalformedResponse {
						t.Fatalf("agent=%v stream=%v usage=%s error=%v, want malformed_response", agent, stream, raw, err)
					}
				}
			}
		})
	}
}
