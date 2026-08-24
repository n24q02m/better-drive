package credentials

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validReference() Reference {
	return Reference{Provider: "r2", Account: "acct-1", Root: "root-1", Bucket: "backup", Endpoint: "https://r2.example.test"}
}

func validBinding() Binding {
	return Binding{Provider: "r2", Account: "acct-1", Root: "root-1", Bucket: "backup", Endpoint: "https://r2.example.test", SessionExpiresAt: time.Unix(200, 0).UTC()}
}

func TestResolverReturnsOnlyBindingMetadataAndSecretStaysBehindSource(t *testing.T) {
	secret := "token-value-must-not-leak"
	source := NewMemorySource()
	ref := validReference()
	if err := source.Put(ref, validBinding(), []byte(secret)); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(source)
	resolver.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	binding, err := resolver.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	serialized, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), secret) {
		t.Fatalf("binding JSON leaked secret: %s", serialized)
	}
	got, err := resolver.ResolveSecret(context.Background(), ref)
	if err != nil {
		t.Fatalf("ResolveSecret() error = %v", err)
	}
	if string(got) != secret {
		t.Fatalf("secret = %q, want %q", got, secret)
	}
}

func TestResolverRejectsWrongBindingAndExpiredSessionWithoutSecretInError(t *testing.T) {
	secret := "wrong-binding-secret"
	ref := validReference()
	wrong := validBinding()
	wrong.Account = "other-account"
	source := &staticSource{binding: wrong, secret: []byte(secret)}
	resolver := NewResolver(source)
	resolver.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	if _, err := resolver.Resolve(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "binding") || strings.Contains(err.Error(), secret) {
		t.Fatalf("wrong binding error = %v", err)
	}

	expired := validBinding()
	expired.SessionExpiresAt = time.Unix(99, 0).UTC()
	source.binding = expired
	if _, err := resolver.Resolve(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), secret) {
		t.Fatalf("expired error = %v", err)
	}
}

type staticSource struct {
	binding Binding
	secret  []byte
}

func (source *staticSource) Resolve(context.Context, Reference) (Binding, error) {
	return source.binding, nil
}

func (source *staticSource) Secret(context.Context, Reference) ([]byte, error) {
	return append([]byte(nil), source.secret...), nil
}

func TestReferenceAndBindingValidateAllBindingFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Reference)
	}{
		{name: "provider", mutate: func(ref *Reference) { ref.Provider = "" }},
		{name: "account", mutate: func(ref *Reference) { ref.Account = "" }},
		{name: "root", mutate: func(ref *Reference) { ref.Root = "" }},
		{name: "bucket", mutate: func(ref *Reference) { ref.Bucket = "" }},
		{name: "endpoint", mutate: func(ref *Reference) { ref.Endpoint = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := validReference()
			tc.mutate(&ref)
			if err := ref.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want %s rejection", tc.name)
			}
		})
	}
	binding := validBinding()
	binding.SessionExpiresAt = time.Time{}
	if err := binding.Validate(time.Unix(100, 0).UTC()); err == nil {
		t.Fatal("Binding.Validate() accepted zero expiry")
	}
}

func TestReferenceAndBindingRejectConflictingCompatibilityAliases(t *testing.T) {
	ref := validReference()
	ref.AccountID = "different-account"
	if err := ref.Validate(); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("conflicting reference account aliases error = %v, want account rejection", err)
	}
	ref = validReference()
	ref.RootID = "different-root"
	if err := ref.Validate(); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("conflicting reference root aliases error = %v, want root rejection", err)
	}

	binding := validBinding()
	binding.AccountID = "different-account"
	if err := binding.Validate(time.Unix(100, 0).UTC()); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("conflicting binding account aliases error = %v, want account rejection", err)
	}
	binding = validBinding()
	binding.RootID = "different-root"
	if err := binding.Validate(time.Unix(100, 0).UTC()); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("conflicting binding root aliases error = %v, want root rejection", err)
	}
}

func TestReferenceAndBindingAcceptMatchingCompatibilityAliases(t *testing.T) {
	ref := validReference()
	ref.AccountID = ref.Account
	ref.RootID = ref.Root
	if err := ref.Validate(); err != nil {
		t.Fatalf("matching reference aliases: %v", err)
	}
	binding := validBinding()
	binding.AccountID = binding.Account
	binding.RootID = binding.Root
	if err := binding.Validate(time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("matching binding aliases: %v", err)
	}
}

func TestResolverRejectsConflictingBindingCompatibilityAliases(t *testing.T) {
	ref := validReference()
	binding := validBinding()
	binding.AccountID = "different-account"
	source := &staticSource{binding: binding, secret: []byte("secret")}
	resolver := NewResolver(source)
	resolver.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	if _, err := resolver.Resolve(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("resolver conflicting alias error = %v, want account rejection", err)
	}
}

func TestSourceHonorsContextCancellation(t *testing.T) {
	source := NewMemorySource()
	ref := validReference()
	if err := source.Put(ref, validBinding(), []byte("secret")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Secret(ctx, ref); err == nil {
		t.Fatal("Secret() accepted canceled context")
	}
}
