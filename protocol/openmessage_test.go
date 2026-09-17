package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOpenMessageMirrorsSubmitRequest(t *testing.T) {
	type member struct {
		typ reflect.Type
		tag string
	}
	collect := func(value any, skip string) map[string]member {
		members := map[string]member{}
		t := reflect.TypeOf(value)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.Name == skip {
				continue
			}
			members[field.Name] = member{typ: field.Type, tag: field.Tag.Get("json")}
		}
		return members
	}
	open := collect(OpenMessage{}, "")
	submit := collect(MessageSubmitRequest{}, "SessionID")

	for name, want := range submit {
		got, ok := open[name]
		if !ok {
			t.Errorf("OpenMessage is missing %s, which a separate submit accepts: a host cannot use it at open", name)
			continue
		}
		if got.typ != want.typ {

			t.Errorf("OpenMessage.%s is %s, MessageSubmitRequest.%s is %s", name, got.typ, name, want.typ)
		}
		if got.tag != want.tag {
			t.Errorf("OpenMessage.%s has json tag %q, MessageSubmitRequest.%s has %q", name, got.tag, name, want.tag)
		}
	}
	for name := range open {
		if _, ok := submit[name]; !ok {
			t.Errorf("OpenMessage carries %s, which no separate submit accepts", name)
		}
	}
}

func TestOpenMessageSubmitCarriesEveryMember(t *testing.T) {
	instructions, model := "be brief", "model-1"
	message := OpenMessage{
		Messages:              []Message{{Role: RoleUser, Content: TextContent("hello")}},
		Delivery:              DeliveryAuto,
		ModelID:               &model,
		Instructions:          &instructions,
		ToolChoice:            []byte(`"auto"`),
		OutputSchema:          []byte(`{"type":"object"}`),
		AllowDegradedFeatures: []string{FeatureOpenSubscribe},
		Metadata:              map[string]json.RawMessage{"k": []byte(`1`)},
	}
	submitted := message.Submit("session-1")
	if submitted.SessionID != "session-1" {
		t.Fatalf("Submit bound session %q, want session-1", submitted.SessionID)
	}
	open := reflect.ValueOf(message)
	projected := reflect.ValueOf(submitted)
	for i := 0; i < open.NumField(); i++ {
		name := open.Type().Field(i).Name
		if open.Field(i).IsZero() {
			t.Fatalf("the fixture leaves %s zero, so the projection of it is untested", name)
		}
		if !reflect.DeepEqual(open.Field(i).Interface(), projected.FieldByName(name).Interface()) {
			t.Errorf("Submit dropped or changed %s", name)
		}
	}
}
