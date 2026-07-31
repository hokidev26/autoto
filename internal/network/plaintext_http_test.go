package network

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
)

// plaintextResolver answers with a public address for the relay host and a
// loopback address for the local host, so each case exercises one branch of the
// PolicyProviderDirect plaintext guard.
func plaintextResolver() Resolver {
	return resolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "relay.example", "154.36.172.121":
			return []net.IPAddr{{IP: net.ParseIP("154.36.172.121")}}, nil
		case "localhost":
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		default:
			return nil, errors.New("not found")
		}
	})
}

func TestProviderDirectDeniesPlaintextHTTPToPublicHostByDefault(t *testing.T) {
	err := ValidateProviderBaseURL(context.Background(), "http://relay.example:3000/v1", WithResolver(plaintextResolver()))
	if !errors.Is(err, ErrDestinationDenied) {
		t.Fatalf("expected ErrDestinationDenied without the opt-in, got %v", err)
	}
}

func TestProviderDirectAllowsPlaintextHTTPWhenExplicitlyAllowed(t *testing.T) {
	err := ValidateProviderBaseURL(
		context.Background(),
		"http://relay.example:3000/v1",
		WithResolver(plaintextResolver()),
		WithPlaintextHTTPAllowed(),
	)
	if err != nil {
		t.Fatalf("expected the opt-in to permit plain HTTP, got %v", err)
	}
}

func TestProviderDirectKeepsLoopbackPlaintextHTTPAllowed(t *testing.T) {
	// Loopback never depended on the opt-in; it must behave identically either way.
	for _, opts := range [][]Option{
		{WithResolver(plaintextResolver())},
		{WithResolver(plaintextResolver()), WithPlaintextHTTPAllowed()},
	} {
		if err := ValidateProviderBaseURL(context.Background(), "http://localhost:3000/v1", opts...); err != nil {
			t.Fatalf("expected loopback plain HTTP to stay allowed, got %v", err)
		}
	}
}

func TestProviderDirectHTTPSUnaffectedByPlaintextOptIn(t *testing.T) {
	for _, opts := range [][]Option{
		{WithResolver(plaintextResolver())},
		{WithResolver(plaintextResolver()), WithPlaintextHTTPAllowed()},
	} {
		if err := ValidateProviderBaseURL(context.Background(), "https://relay.example:3000/v1", opts...); err != nil {
			t.Fatalf("expected HTTPS to stay allowed, got %v", err)
		}
	}
}

func TestPlaintextOptInDoesNotLeakIntoOtherPolicies(t *testing.T) {
	parsed, err := url.Parse("http://relay.example:3000/v1")
	if err != nil {
		t.Fatal(err)
	}
	// PolicyPublicDirect has no plaintext rule of its own; the option must not
	// change its behaviour in either direction.
	withoutOptIn := ValidateURL(context.Background(), PolicyPublicDirect, parsed, WithResolver(plaintextResolver()))
	withOptIn := ValidateURL(context.Background(), PolicyPublicDirect, parsed, WithResolver(plaintextResolver()), WithPlaintextHTTPAllowed())
	if !errors.Is(withoutOptIn, withOptIn) && withoutOptIn != withOptIn {
		t.Fatalf("public direct policy changed with the opt-in: %v vs %v", withoutOptIn, withOptIn)
	}
}
