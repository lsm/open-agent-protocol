package adapter_test

import (
	"errors"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestANonAutoDeliveryIsJudgedUnderItsOwnKeyAfterTheRunControlsAndAutoIsNeverGated(t *testing.T) {
	for _, mode := range []struct {
		delivery protocol.RequestedDeliveryMode
		key      string
	}{
		{protocol.DeliveryQueue, protocol.FeatureDeliveryQueue},
		{protocol.DeliverySteer, protocol.FeatureDeliverySteer},
		{protocol.DeliveryBTW, protocol.FeatureDeliveryBTW},
	} {
		var refused *adapter.UnsupportedControlError
		err := adapter.RefuseUnadvertisedControls(protocol.MessageSubmitRequest{Delivery: mode.delivery})
		if !errors.As(err, &refused) || refused.Feature != mode.key || refused.Reason != adapter.ControlUnadvertised {
			t.Fatalf("%s: err = %v", mode.delivery, err)
		}
		if err := adapter.RefuseUnadvertisedControls(protocol.MessageSubmitRequest{Delivery: mode.delivery}, mode.key); err != nil {
			t.Fatalf("%s advertised: err = %v", mode.delivery, err)
		}
	}

	var refused *adapter.UnsupportedControlError
	err := adapter.RefuseUnadvertisedControls(protocol.MessageSubmitRequest{Delivery: protocol.DeliveryQueue, Instructions: protocol.ControlValue("i")})
	if !errors.As(err, &refused) || refused.Feature != protocol.FeatureInstructions {
		t.Fatalf("instructed queue: err = %v", err)
	}

	for _, delivery := range []protocol.RequestedDeliveryMode{"", protocol.DeliveryAuto} {
		if err := adapter.RefuseUnadvertisedControls(protocol.MessageSubmitRequest{Delivery: delivery}); err != nil {
			t.Fatalf("%q: err = %v", delivery, err)
		}
	}
}
