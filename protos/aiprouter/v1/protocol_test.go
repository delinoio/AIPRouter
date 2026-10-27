// SPDX-License-Identifier: Apache-2.0
package aiprouterv1_test

import (
	"testing"

	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestUnknownUsageAndPricePresence(t *testing.T) {
	for _, known := range []bool{false, true} {
		value := &v1.TokenUsage{}
		if known {
			value.InputTokens = proto.Uint64(0)
		}
		wire, err := proto.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		decoded := &v1.TokenUsage{}
		if err := proto.Unmarshal(wire, decoded); err != nil {
			t.Fatal(err)
		}
		if (decoded.InputTokens != nil) != known {
			t.Fatal("usage presence lost")
		}
		json, err := protojson.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if !known && string(json) != "{}" {
			t.Fatalf("unknown serialized as known: %s", json)
		}
	}
	rates := &v1.PriceRates{Input: proto.String("0")}
	wire, _ := proto.Marshal(rates)
	decoded := &v1.PriceRates{}
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Input == nil || decoded.Output != nil {
		t.Fatal("price presence lost")
	}
}

func TestEveryMethodDeclaresAuthenticationScope(t *testing.T) {
	files := []protoreflect.FileDescriptor{v1.File_aiprouter_v1_organization_proto, v1.File_aiprouter_v1_device_proto, v1.File_aiprouter_v1_provider_proto, v1.File_aiprouter_v1_usage_proto, v1.File_aiprouter_v1_operator_proto}
	for _, file := range files {
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			for j := 0; j < service.Methods().Len(); j++ {
				method := service.Methods().Get(j)
				scope, ok := proto.GetExtension(method.Options(), v1.E_AuthScope).(v1.AuthScope)
				if !ok || scope == v1.AuthScope_AUTH_SCOPE_UNSPECIFIED {
					t.Errorf("missing scope: %s", method.FullName())
				}
				if scope == v1.AuthScope_AUTH_SCOPE_PUBLIC && method.Name() != "BeginDeviceAuthorization" {
					t.Errorf("unexpected public method: %s", method.FullName())
				}
				if scope == v1.AuthScope_AUTH_SCOPE_INFERENCE_KEY && service.Name() != "ClientService" {
					t.Errorf("inference credential grants management: %s", method.FullName())
				}
				if scope == v1.AuthScope_AUTH_SCOPE_DEVICE_SECRET && service.Name() != "DeviceAuthorizationService" {
					t.Errorf("device credential grants management: %s", method.FullName())
				}
				if scope == v1.AuthScope_AUTH_SCOPE_ORGANIZATION_MEMBER || scope == v1.AuthScope_AUTH_SCOPE_ORGANIZATION_ADMINISTRATOR || scope == v1.AuthScope_AUTH_SCOPE_ORGANIZATION_OWNER {
					if method.Input().Fields().ByName("organization_id") == nil {
						t.Errorf("missing tenant selector: %s", method.FullName())
					}
				}
			}
		}
	}
}

func TestPrivateResourcesAndOperatorBoundary(t *testing.T) {
	for _, name := range []protoreflect.Name{"ProviderConnection", "Pool", "Route"} {
		message := v1.File_aiprouter_v1_provider_proto.Messages().ByName(name)
		for _, forbidden := range []protoreflect.Name{"api_key", "raw_key", "access_token", "refresh_token", "prompt", "response"} {
			if message.Fields().ByName(forbidden) != nil {
				t.Errorf("secret in read model %s.%s", name, forbidden)
			}
		}
	}
	service := v1.File_aiprouter_v1_operator_proto.Services().ByName("OperatorService")
	for i := 0; i < service.Methods().Len(); i++ {
		method := service.Methods().Get(i)
		for _, forbidden := range []protoreflect.Name{"organization_id", "user_id"} {
			if method.Input().Fields().ByName(forbidden) != nil {
				t.Errorf("operator inspection selector: %s", method.FullName())
			}
		}
	}
	request := (&v1.RevokeDeviceKeyRequest{}).ProtoReflect().Descriptor()
	if request.Fields().ByName("key_id") != nil {
		t.Fatal("device compensation may not select an arbitrary key")
	}
}
