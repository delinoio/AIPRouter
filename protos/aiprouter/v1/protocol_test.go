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

func TestRecipientInvitationAndDeviceContracts(t *testing.T) {
	service := v1.File_aiprouter_v1_organization_proto.Services().ByName("InvitationService")
	for _, name := range []protoreflect.Name{"GetInvitation", "ListMyInvitations", "AcceptInvitation"} {
		method := service.Methods().ByName(name)
		if method == nil {
			t.Fatalf("missing recipient method %s", name)
		}
		if proto.GetExtension(method.Options(), v1.E_AuthScope) != v1.AuthScope_AUTH_SCOPE_WEB_SESSION {
			t.Fatalf("recipient method must require session: %s", name)
		}
	}
	list := (&v1.ListMyInvitationsRequest{}).ProtoReflect().Descriptor()
	if list.Fields().Len() != 1 || list.Fields().ByName("page") == nil {
		t.Fatal("recipient listing must not accept a caller-selected email or user")
	}
	invite := (&v1.Invitation{}).ProtoReflect().Descriptor()
	for _, name := range []protoreflect.Name{"token", "raw_token", "acceptance_url"} {
		if invite.Fields().ByName(name) != nil {
			t.Fatalf("recipient list exposes token: %s", name)
		}
	}
	for _, state := range []v1.LifecycleState{v1.LifecycleState_LIFECYCLE_STATE_PENDING, v1.LifecycleState_LIFECYCLE_STATE_ACCEPTED, v1.LifecycleState_LIFECYCLE_STATE_CANCELED, v1.LifecycleState_LIFECYCLE_STATE_EXPIRED, v1.LifecycleState_LIFECYCLE_STATE_SUPERSEDED, v1.LifecycleState_LIFECYCLE_STATE_FAILED} {
		original := &v1.GetInvitationResponse{Invitation: &v1.Invitation{Id: "invite", OrganizationId: "org", OrganizationName: "Organization", Email: "recipient@example.test", Role: v1.Role_ROLE_MEMBER, State: state}}
		wire, err := proto.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded := &v1.GetInvitationResponse{}
		if err = proto.Unmarshal(wire, decoded); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(original, decoded) {
			t.Fatal("invitation detail or lifecycle lost")
		}
	}
	for _, request := range []*v1.AcceptInvitationRequest{{Token: "link-token"}, {InvitationId: proto.String("invite")}} {
		wire, err := proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		decoded := &v1.AcceptInvitationRequest{}
		if err = proto.Unmarshal(wire, decoded); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(request, decoded) {
			t.Fatal("acceptance selector compatibility lost")
		}
	}
	for number := int32(1); number <= 6; number++ {
		original := &v1.DeviceAuthorization{State: v1.DeviceAuthorizationState(number)}
		wire, err := proto.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded := &v1.DeviceAuthorization{}
		if err = proto.Unmarshal(wire, decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.State != original.State {
			t.Fatal("browser device lifecycle lost")
		}
	}
}

func TestOptionalFiltersPreserveExplicitUnknown(t *testing.T) {
	messages := []proto.Message{&v1.ListUsageRequest{}, &v1.GetUsageSummaryRequest{}, &v1.ListDailyUsageRequest{}, &v1.ListOrganizationAuditRequest{}, &v1.ListInstanceAuditRequest{}}
	for _, message := range messages {
		field := message.ProtoReflect().Descriptor().Fields().ByName("outcome")
		if field == nil || !field.HasPresence() {
			t.Fatalf("missing outcome presence: %T", message)
		}
		for _, present := range []bool{false, true} {
			value := proto.Clone(message)
			if present {
				value.ProtoReflect().Set(field, protoreflect.ValueOfEnum(0))
			}
			wire, err := proto.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			decoded := message.ProtoReflect().New().Interface()
			if err = proto.Unmarshal(wire, decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.ProtoReflect().Has(field) != present {
				t.Fatalf("unknown outcome presence lost: %T", message)
			}
		}
	}
	if (&v1.AuditRecord{}).Outcome != v1.AuditOutcome_AUDIT_OUTCOME_UNSPECIFIED {
		t.Fatal("missing audit outcome must remain unknown")
	}
	if (&v1.InstanceHealth{}).UsageRecording != v1.HealthState_HEALTH_STATE_UNSPECIFIED {
		t.Fatal("missing recording health must remain unknown")
	}
}

func TestOAuthOptionsAndCollectionRevisionRoundTrip(t *testing.T) {
	values := []proto.Message{
		&v1.Pool{AffinityEnabled: true},
		&v1.CreatePoolRequest{AffinityEnabled: true},
		&v1.UpdatePoolRequest{AffinityEnabled: true},
		&v1.ProviderDefinition{Id: "native-provider", OauthMethods: []v1.ProviderOAuthMethod{v1.ProviderOAuthMethod_PROVIDER_O_AUTH_METHOD_NATIVE_CALLBACK}},
		&v1.BeginProviderOAuthRequest{Method: v1.ProviderOAuthMethod_PROVIDER_O_AUTH_METHOD_NATIVE_CALLBACK.Enum(), RedirectUri: proto.String("aiprouter://callback"), LoginProvider: proto.String("identity-center"), SsoRegion: proto.String("region"), ApiRegion: proto.String("region"), StartUrl: proto.String("https://start.example.test"), BaseUrl: proto.String("https://api.example.test"), AuthBaseUrl: proto.String("https://auth.example.test"), PortalUrl: proto.String("https://portal.example.test"), RegisterUrl: proto.String("https://register.example.test"), ClientId: proto.String("client")},
		&v1.ProviderOAuth{CallbackState: proto.String("public-correlation-state")},
		&v1.ListPriceOverridesResponse{Revision: "collection-1"},
		&v1.SetPriceOverrideRequest{ExpectedRevision: "collection-1"},
		&v1.SetPriceOverrideResponse{Revision: "collection-2"},
		&v1.DeletePriceOverrideRequest{ExpectedRevision: "collection-2"},
		&v1.DeletePriceOverrideResponse{Revision: "collection-3"},
	}
	for _, value := range values {
		wire, err := proto.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		decoded := value.ProtoReflect().New().Interface()
		if err = proto.Unmarshal(wire, decoded); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(value, decoded) {
			t.Fatalf("new fields lost: %T", value)
		}
	}
}
