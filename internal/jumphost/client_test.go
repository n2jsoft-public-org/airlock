package jumphost

import (
	"context"
	"errors"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/dbmcp"
	"github.com/n2jsoft-public-org/airlock/internal/dockerdb"
)

func TestDockerSessionClientCarriesItsPassword(t *testing.T) {
	session, err := dockerRunner(t, pgContainer()).Prepare(context.Background(), Options{ResourceType: "docker", Name: "pg", SkipMCP: true, SkipIDEDataSource: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	client, err := session.Client(context.Background(), &SignInCandidate{Label: "password", User: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	want := Client{Engine: "postgres", Address: dbmcp.Address{Host: "127.0.0.1", Port: 55432, TLS: dbmcp.TLSDisable},
		User: "postgres", Password: "secret", Database: "appdb"}
	if client != want {
		t.Fatalf("client = %+v, want %+v", client, want)
	}
}

func TestDockerCacheClientWithoutAuth(t *testing.T) {
	redis := dockerdb.Container{Name: "redis", Engine: dockerdb.EngineRedis, Host: "127.0.0.1", Port: 56379, Trust: true}
	session, err := dockerRunner(t, redis).Prepare(context.Background(), Options{ResourceType: "docker", Name: "redis", SkipMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	client, err := session.Client(context.Background(), &SignInCandidate{Label: "no auth"})
	if err != nil {
		t.Fatal(err)
	}
	if client.Engine != ClientEngineRedis || client.Address.Port != 56379 || client.Address.TLS != dbmcp.TLSDisable || client.User != "" || client.Password != "" {
		t.Fatalf("client = %+v", client)
	}
}

func TestTunnelClientWithoutEngineOnlyHasTheAddress(t *testing.T) {
	session := &Session{LocalPort: 16443}
	client, err := session.Client(context.Background(), nil)
	if err != nil || client != (Client{Address: dbmcp.TunnelAddress(16443)}) {
		t.Fatalf("Client = %+v, %v", client, err)
	}
	client, err = rdsClient(nil, &ideTarget{Engine: "oracle-ee"}, 11521)(context.Background(), nil)
	if err != nil || client.Engine != "" || client.Address.Port != 11521 {
		t.Fatalf("oracle client = %+v, %v", client, err)
	}
}

func TestRDSClientSignsInWithThePassword(t *testing.T) {
	target := ideTarget{Engine: "postgres", Database: "app", DBUser: "app_rw", DBPassword: "p@ss"}
	client, err := rdsClient(nil, &target, 15432)(context.Background(), &SignInCandidate{Label: "password", User: "app_rw"})
	if err != nil {
		t.Fatal(err)
	}
	if client.Engine != "postgres" || client.User != "app_rw" || client.Password != "p@ss" || client.Database != "app" || client.Address != dbmcp.TunnelAddress(15432) {
		t.Fatalf("client = %+v", client)
	}
}

func TestCacheClientNeedsAChosenSignIn(t *testing.T) {
	target := ideTarget{Engine: "redis", CacheAuth: CacheAuthRBAC, CacheTLS: true, CacheEndpoint: "master.cache.amazonaws.com"}
	_, err := testRunner(t).cacheClient("cache", &target, 16379, awsConfig{Profile: "dev"})(context.Background(), nil)
	if !errors.Is(err, errCacheSignInRequired) {
		t.Fatalf("err = %v, want errCacheSignInRequired", err)
	}
	target.CacheAuth = CacheAuthNone
	client, err := testRunner(t).cacheClient("cache", &target, 16379, awsConfig{Profile: "dev"})(context.Background(), nil)
	if err != nil || client.ServerName != "master.cache.amazonaws.com" || client.Address.TLS == dbmcp.TLSDisable {
		t.Fatalf("client = %+v, %v", client, err)
	}
}
