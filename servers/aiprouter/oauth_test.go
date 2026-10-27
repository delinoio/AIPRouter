// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"github.com/delinoio/AIPRouter/packages/aiprouter/providers"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
)

type oauthTestExecutor func(context.Context, core.Scope, *http.Request) (*http.Response, error)

func (f oauthTestExecutor) Do(c context.Context, s core.Scope, r *http.Request) (*http.Response, error) {
	return f(c, s, r)
}
func oauthResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func oauthFixture(t *testing.T) (*management, context.Context, core.Scope) {
	t.Helper()
	s := serverStore(t)
	u, _ := testIdentity(t, s, "oauth")
	ctx := context.WithValue(context.Background(), identityKey{}, Identity{UserID: u.ID})
	org, err := s.CreateOrganization(ctx, u.ID, "oauth")
	if err != nil {
		t.Fatal(err)
	}
	sc, _, err := s.Membership(ctx, org.ID, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &management{store: s}, ctx, sc
}
func beginAnthropic(t *testing.T, m *management, ctx context.Context, sc core.Scope, connection string) *v1.ProviderOAuth {
	t.Helper()
	q := &v1.BeginProviderOAuthRequest{OrganizationId: sc.OrganizationID, ProviderId: "anthropic", Name: "Personal"}
	if connection != "" {
		q.ConnectionId = &connection
	}
	result, err := m.BeginProviderOAuth(ctx, connect.NewRequest(q))
	if err != nil {
		t.Fatal(err)
	}
	return result.Msg.Authorization
}
func TestProviderOAuthDurableCompletionAndIsolation(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	m.providerExecutor = oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		return oauthResponse(`{"access_token":"private-access","refresh_token":"private-refresh","expires_in":3600}`), nil
	})
	flow := beginAnthropic(t, m, ctx, sc, "")
	if strings.Contains(flow.String(), "Verifier") || flow.GetCallbackState() == "" {
		t.Fatal("unsafe or missing challenge")
	}
	// A fresh management instance consumes the durable flow, without process state.
	other := &management{store: m.store, providerExecutor: m.providerExecutor}
	done, err := other.CompleteProviderOAuth(ctx, connect.NewRequest(&v1.CompleteProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id, Code: "code", State: flow.GetCallbackState()}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(done.Msg.String(), "private-") {
		t.Fatal("secret in response")
	}
	got, err := m.GetProviderOAuth(ctx, connect.NewRequest(&v1.GetProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id}))
	if err != nil || got.Msg.Authorization.State != v1.OAuthState_O_AUTH_STATE_COMPLETED {
		t.Fatalf("receipt: %v %v", got, err)
	}
	if _, err = m.CompleteProviderOAuth(ctx, connect.NewRequest(&v1.CompleteProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id, Code: "code", State: flow.GetCallbackState()})); err == nil {
		t.Fatal("replayed flow")
	}
	u, _ := testIdentity(t, m.store, "other")
	foreign := context.WithValue(ctx, identityKey{}, Identity{UserID: u.ID})
	if _, err = m.GetProviderOAuth(foreign, connect.NewRequest(&v1.GetProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id})); err == nil {
		t.Fatal("cross-user read")
	}
}
func TestProviderOAuthCancelFencesInflightCompletion(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	reader, writer := io.Pipe()
	started := make(chan struct{})
	m.providerExecutor = oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		close(started)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}, nil
	})
	flow := beginAnthropic(t, m, ctx, sc, "")
	done := make(chan error, 1)
	go func() {
		_, err := m.CompleteProviderOAuth(ctx, connect.NewRequest(&v1.CompleteProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id, Code: "code", State: flow.GetCallbackState()}))
		done <- err
	}()
	<-started
	if _, err := m.CancelProviderOAuth(ctx, connect.NewRequest(&v1.CancelProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id})); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte(`{"access_token":"late-secret","refresh_token":"rotated","expires_in":3600}`))
	_ = writer.Close()
	if err := <-done; err == nil {
		t.Fatal("canceled flow completed")
	}
	ids, err := m.store.ListPrivate(ctx, sc, string(providerKind))
	if err != nil || len(ids) != 0 {
		t.Fatalf("resurrected provider: %v %v", ids, err)
	}
	receipt, err := m.GetProviderOAuth(ctx, connect.NewRequest(&v1.GetProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id}))
	if err != nil || receipt.Msg.Authorization.State != v1.OAuthState_O_AUTH_STATE_CANCELED {
		t.Fatalf("cancel receipt: %v %v", receipt, err)
	}
}
func TestProviderOAuthReconnectCannotResurrectDeletedConnection(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := oauthSavedProvider(t, m, ctx, sc)
	flow := beginAnthropic(t, m, ctx, sc, d.Metadata.Id)
	if err := m.store.DeletePrivate(ctx, sc, string(providerKind), d.Metadata.Id); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	m.providerExecutor = oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		sends.Add(1)
		return oauthResponse(`{"access_token":"late","refresh_token":"rotated"}`), nil
	})
	if _, err := m.CompleteProviderOAuth(ctx, connect.NewRequest(&v1.CompleteProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: flow.Id, Code: "code", State: flow.GetCallbackState()})); err == nil {
		t.Fatal("deleted connection recreated")
	}
	if sends.Load() != 0 {
		t.Fatal("deleted connection exchanged credentials")
	}
	if _, err := m.store.GetPrivate(ctx, sc, string(providerKind), d.Metadata.Id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
}
func oauthSavedProvider(t *testing.T, m *management, ctx context.Context, sc core.Scope) providerData {
	t.Helper()
	d := providerData{Metadata: &v1.ProviderConnection{Id: core.ID(), OrganizationId: sc.OrganizationID, ProviderId: "anthropic", Name: "private", BaseUrl: "https://api.anthropic.com", AuthKind: v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH, State: v1.ConnectionState_CONNECTION_STATE_READY}, Credential: &providers.Credential{Scope: sc, Provider: "anthropic", AccessToken: "old-token", RefreshToken: "rotating-token", ExpiresAt: time.Now().Add(-time.Minute)}}
	if err := m.put(ctx, sc, providerKind, d.Metadata.Id, d); err != nil {
		t.Fatal(err)
	}
	return d
}
func oauthLease(t *testing.T, m *management, ctx context.Context, sc core.Scope) store.Lease {
	t.Helper()
	_, key, err := m.store.IssueKey(ctx, sc, "refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := m.store.Admit(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.store.Release(context.Background(), lease) })
	return lease
}
func TestProviderRefreshUncertainNeverRetries(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := oauthSavedProvider(t, m, ctx, sc)
	lease := oauthLease(t, m, ctx, sc)
	var sends atomic.Int32
	executor := oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		sends.Add(1)
		return nil, errors.New("ambiguous fixture transport")
	})
	if _, err := refreshProviderCredentialWithExecutor(ctx, m.store, lease, d.Metadata.Id, executor); err == nil {
		t.Fatal("ambiguous accepted")
	}
	if _, err := refreshProviderCredentialWithExecutor(ctx, m.store, lease, d.Metadata.Id, executor); !errors.Is(err, store.ErrReauthentication) {
		t.Fatalf("retry: %v", err)
	}
	if sends.Load() != 1 {
		t.Fatalf("rotating token sent %d times", sends.Load())
	}
	listed, listErr := m.ListProviderConnections(ctx, connect.NewRequest(&v1.ListProviderConnectionsRequest{OrganizationId: sc.OrganizationID}))
	if listErr != nil || len(listed.Msg.Connections) != 1 || listed.Msg.Connections[0].State != v1.ConnectionState_CONNECTION_STATE_AUTHORIZATION_REQUIRED {
		t.Fatalf("uncertain login not visible: %v %v", listed, listErr)
	}

	if _, err := m.store.GetPrivate(ctx, sc, string(providerKind), d.Metadata.Id); !errors.Is(err, store.ErrReauthentication) {
		t.Fatalf("uncertain usable: %v", err)
	}
}
func TestProviderRefreshPersistsRotatedToken(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := oauthSavedProvider(t, m, ctx, sc)
	lease := oauthLease(t, m, ctx, sc)
	executor := oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		return oauthResponse(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`), nil
	})
	fresh, err := refreshProviderCredentialWithExecutor(ctx, m.store, lease, d.Metadata.Id, executor)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Credential.AccessToken != "new-token" {
		t.Fatal("token not rotated")
	}
	raw, err := m.store.GetPrivate(ctx, sc, string(providerKind), d.Metadata.Id)
	if err != nil {
		t.Fatal(err)
	}
	var saved providerData
	if json.Unmarshal(raw, &saved) != nil || saved.Credential.RefreshToken != "new-refresh" {
		t.Fatal("rotated token not durable")
	}
}

func TestProviderRefreshReplacementFencesLateResponse(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := oauthSavedProvider(t, m, ctx, sc)
	lease := oauthLease(t, m, ctx, sc)
	reader, writer := io.Pipe()
	started := make(chan struct{})
	var sends atomic.Int32
	executor := oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		if sends.Add(1) == 1 {
			close(started)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}, nil
		}
		return oauthResponse(`{"data":[]}`), nil
	})
	result := make(chan error, 1)
	go func() {
		_, err := refreshProviderCredentialWithExecutor(ctx, m.store, lease, d.Metadata.Id, executor)
		result <- err
	}()
	<-started
	replacement := d
	copyCredential := *d.Credential
	copyCredential.AccessToken = "explicit-replacement"
	replacement.Credential = &copyCredential
	if err := m.put(ctx, sc, providerKind, d.Metadata.Id, replacement); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte(`{"access_token":"late-token","refresh_token":"late-refresh"}`))
	_ = writer.Close()
	if err := <-result; err == nil {
		t.Fatal("stale refresh accepted")
	}
	raw, err := m.store.GetPrivate(ctx, sc, string(providerKind), d.Metadata.Id)
	if err != nil {
		t.Fatal(err)
	}
	var saved providerData
	if json.Unmarshal(raw, &saved) != nil || saved.Credential.AccessToken != "explicit-replacement" {
		t.Fatal("replacement overwritten")
	}
	if sends.Load() != 1 {
		t.Fatal("stale refresh sent catalog request")
	}
}
func TestProviderPoolAndRoutingValidation(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := oauthSavedProvider(t, m, ctx, sc)
	for _, strategy := range []v1.PoolStrategy{v1.PoolStrategy_POOL_STRATEGY_PRIORITY, v1.PoolStrategy_POOL_STRATEGY_ROUND_ROBIN, v1.PoolStrategy_POOL_STRATEGY_LEAST_RECENTLY_USED} {
		if err := m.validatePool(ctx, sc, &v1.Pool{Name: "pool", Strategy: strategy, Members: []*v1.PoolMember{{ConnectionId: d.Metadata.Id}}}); err != nil {
			t.Fatal(err)
		}
	}
	target := &v1.RouteTarget{Target: &v1.RouteTarget_ConnectionId{ConnectionId: d.Metadata.Id}, Weight: 1}
	for _, strategy := range []v1.RoutingStrategy{v1.RoutingStrategy_ROUTING_STRATEGY_DIRECT, v1.RoutingStrategy_ROUTING_STRATEGY_FALLBACK, v1.RoutingStrategy_ROUTING_STRATEGY_WEIGHTED, v1.RoutingStrategy_ROUTING_STRATEGY_ROUND_ROBIN} {
		if err := m.validateRoute(ctx, sc, &v1.Route{ModelId: "model", Strategy: strategy, Targets: []*v1.RouteTarget{target}}); err != nil {
			t.Fatal(err)
		}
	}
	target.Weight = 0
	if err := m.validateRoute(ctx, sc, &v1.Route{ModelId: "model", Strategy: v1.RoutingStrategy_ROUTING_STRATEGY_WEIGHTED, Targets: []*v1.RouteTarget{target}}); err == nil {
		t.Fatal("zero weighted target")
	}
	if err := m.validatePool(ctx, sc, &v1.Pool{Name: "pool", Strategy: v1.PoolStrategy_POOL_STRATEGY_PRIORITY, Members: []*v1.PoolMember{{ConnectionId: d.Metadata.Id}, {ConnectionId: d.Metadata.Id}}}); err == nil {
		t.Fatal("duplicate member")
	}
}

func TestProviderOAuthPendingPollPersistsInterval(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	id := core.ID()
	connection := core.ID()
	expires := time.Now().Add(time.Minute)
	f := providerOAuthFlow{Flow: providers.Flow{Scope: sc, Provider: "kimi", Method: providers.Device, State: "state", DeviceCode: "private-device-code", ExpiresAt: expires, Interval: 5 * time.Second}, Challenge: providers.Challenge{State: "state", ExpiresAt: expires}, Connection: providerData{Metadata: &v1.ProviderConnection{Id: connection, OrganizationId: sc.OrganizationID, ProviderId: "kimi", Name: "kimi", AuthKind: v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH, State: v1.ConnectionState_CONNECTION_STATE_READY}}}
	raw, _ := json.Marshal(f)
	if err := m.store.SaveProviderFlowBound(ctx, sc, id, string(providerKind), connection, "", raw, expires); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	m.providerExecutor = oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		sends.Add(1)
		r := oauthResponse(`{"error":"slow_down"}`)
		r.StatusCode = 400
		return r, nil
	})
	for range 2 {
		response, err := m.GetProviderOAuth(ctx, connect.NewRequest(&v1.GetProviderOAuthRequest{OrganizationId: sc.OrganizationID, AuthorizationId: id}))
		if err != nil || response.Msg.Authorization.State != v1.OAuthState_O_AUTH_STATE_PENDING {
			t.Fatalf("poll: %v %v", response, err)
		}
	}
	if sends.Load() != 1 {
		t.Fatal("ignored persisted poll interval")
	}
	raw, err := m.store.GetProviderFlow(ctx, sc, id)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &f) != nil || f.Flow.Interval != 10*time.Second || !f.Flow.ExpiresAt.Equal(expires) {
		t.Fatal("poll state or original expiry changed")
	}
}

func TestProviderRefreshRevokedKeyCannotSend(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := oauthSavedProvider(t, m, ctx, sc)
	lease := oauthLease(t, m, ctx, sc)
	if err := m.store.RevokeKey(ctx, sc, lease.Principal.KeyID); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int32
	executor := oauthTestExecutor(func(context.Context, core.Scope, *http.Request) (*http.Response, error) {
		sends.Add(1)
		return oauthResponse(`{}`), nil
	})
	if _, err := refreshProviderCredentialWithExecutor(ctx, m.store, lease, d.Metadata.Id, executor); err == nil {
		t.Fatal("revoked key refreshed")
	}
	if sends.Load() != 0 {
		t.Fatal("revoked key sent")
	}
}
func TestProviderCatalogPersistsOnlyCurrentScope(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := providerData{Metadata: &v1.ProviderConnection{Id: core.ID(), OrganizationId: sc.OrganizationID, ProviderId: "openai", Name: "native", BaseUrl: "https://chatgpt.com/backend-api/codex", AuthKind: v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH, State: v1.ConnectionState_CONNECTION_STATE_READY}, Credential: &providers.Credential{Scope: sc, Provider: "openai", AccessToken: "native-token", AccountID: "account"}}
	if err := m.put(ctx, sc, providerKind, d.Metadata.Id, d); err != nil {
		t.Fatal(err)
	}
	const row = `{"slug":"gpt-native","display_name":"Native","context_window":12345,"instructions":"provider-supplied-template","future_field":{"enabled":false}}`
	m.providerExecutor = oauthTestExecutor(func(_ context.Context, scope core.Scope, r *http.Request) (*http.Response, error) {
		if scope != sc || r.Header.Get("Authorization") != "Bearer native-token" || r.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Error("catalog ownership mismatch")
		}
		return oauthResponse(`{"models":[` + row + `]}`), nil
	})
	result, err := m.ListProviderModels(ctx, connect.NewRequest(&v1.ListProviderModelsRequest{OrganizationId: sc.OrganizationID, ConnectionId: d.Metadata.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Msg.Models) != 1 || result.Msg.Models[0].Id != "gpt-native" {
		t.Fatal("catalog absent")
	}
	raw, err := m.store.GetPrivate(ctx, sc, string(providerKind), d.Metadata.Id)
	if err != nil {
		t.Fatal(err)
	}
	var saved providerData
	if json.Unmarshal(raw, &saved) != nil || saved.Models == nil || saved.ModelsDiscoveredAt.IsZero() || len(saved.CodexCatalog) != 1 || string(saved.CodexCatalog[0]) != row {
		t.Fatal("exact native metadata not persisted")
	}
	if strings.Contains(result.Msg.String(), "provider-supplied-template") || strings.Contains(result.Msg.String(), "native-token") {
		t.Fatal("private fields in management model response")
	}
}

func TestProviderPoolAffinityPersistsExplicitOptIn(t *testing.T) {
	m, ctx, sc := oauthFixture(t)
	d := oauthSavedProvider(t, m, ctx, sc)
	members := []*v1.PoolMember{{ConnectionId: d.Metadata.Id}}
	created, err := m.CreatePool(ctx, connect.NewRequest(&v1.CreatePoolRequest{OrganizationId: sc.OrganizationID, Name: "affinity", Strategy: v1.PoolStrategy_POOL_STRATEGY_PRIORITY, Members: members, AffinityEnabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := m.ListPools(ctx, connect.NewRequest(&v1.ListPoolsRequest{OrganizationId: sc.OrganizationID}))
	if err != nil || len(listed.Msg.Pools) != 1 || !listed.Msg.Pools[0].GetAffinityEnabled() {
		t.Fatalf("affinity opt-in not durable: %v %v", listed, err)
	}
	_, err = m.UpdatePool(ctx, connect.NewRequest(&v1.UpdatePoolRequest{OrganizationId: sc.OrganizationID, PoolId: created.Msg.Pool.Id, Name: "affinity", Strategy: v1.PoolStrategy_POOL_STRATEGY_PRIORITY, Members: members}))
	if err != nil {
		t.Fatal(err)
	}
	listed, err = m.ListPools(ctx, connect.NewRequest(&v1.ListPoolsRequest{OrganizationId: sc.OrganizationID}))
	if err != nil || len(listed.Msg.Pools) != 1 || listed.Msg.Pools[0].GetAffinityEnabled() {
		t.Fatalf("default false did not disable affinity: %v %v", listed, err)
	}
}

func TestProviderListSurvivesAbandonedRefreshAcrossStores(t *testing.T) {
	dsn := os.Getenv("AIPROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires PostgreSQL")
	}
	ctx := context.Background()
	keys, err := core.NewEnvelope("test", map[string][]byte{"test": make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "oauth_" + strings.ReplaceAll(core.ID(), "-", "")
	options := store.Options{Schema: schema, Keys: keys}
	first, err := store.Open(ctx, dsn, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = first.Close()
		db, e := sql.Open("pgx", dsn)
		if e == nil {
			defer db.Close()
			_, _ = db.Exec("DROP SCHEMA IF EXISTS " + schema + "_temp CASCADE")
			_, _ = db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
		}
	})
	if err = first.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user, _ := testIdentity(t, first, "restart")
	ctx = context.WithValue(ctx, identityKey{}, Identity{UserID: user.ID})
	org, err := first.CreateOrganization(ctx, user.ID, "restart")
	if err != nil {
		t.Fatal(err)
	}
	scope, _, err := first.Membership(ctx, org.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := &management{store: first}
	blocked := oauthSavedProvider(t, m, ctx, scope)
	healthy := oauthSavedProvider(t, m, ctx, scope)
	if _, _, _, err = first.ClaimScopePrivateRefresh(ctx, scope, string(providerKind), blocked.Metadata.Id); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := store.Open(ctx, dsn, options)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	restarted := &management{store: second}
	listed, err := restarted.ListProviderConnections(ctx, connect.NewRequest(&v1.ListProviderConnectionsRequest{OrganizationId: scope.OrganizationID}))
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]v1.ConnectionState{}
	for _, row := range listed.Msg.Connections {
		states[row.Id] = row.State
	}
	if len(states) != 2 || states[blocked.Metadata.Id] != v1.ConnectionState_CONNECTION_STATE_AUTHORIZATION_REQUIRED || states[healthy.Metadata.Id] != v1.ConnectionState_CONNECTION_STATE_READY {
		t.Fatalf("unexpected metadata states: %v", states)
	}
	if strings.Contains(listed.Msg.String(), "rotating-token") {
		t.Fatal("secret exposed")
	}
	if _, _, _, err = second.ClaimScopePrivateRefresh(ctx, scope, string(providerKind), blocked.Metadata.Id); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("listing released claim: %v", err)
	}
	if _, err = second.GetPrivate(ctx, scope, string(providerKind), blocked.Metadata.Id); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("credential became usable: %v", err)
	}
	flow := beginAnthropic(t, restarted, ctx, scope, blocked.Metadata.Id)
	if _, err = restarted.CancelProviderOAuth(ctx, connect.NewRequest(&v1.CancelProviderOAuthRequest{OrganizationId: scope.OrganizationID, AuthorizationId: flow.Id})); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.DeleteProviderConnection(ctx, connect.NewRequest(&v1.DeleteProviderConnectionRequest{OrganizationId: scope.OrganizationID, ConnectionId: blocked.Metadata.Id})); err != nil {
		t.Fatal(err)
	}
	listed, err = restarted.ListProviderConnections(ctx, connect.NewRequest(&v1.ListProviderConnectionsRequest{OrganizationId: scope.OrganizationID}))
	if err != nil || len(listed.Msg.Connections) != 1 || listed.Msg.Connections[0].Id != healthy.Metadata.Id {
		t.Fatalf("delete unavailable: %v %v", listed, err)
	}
}
