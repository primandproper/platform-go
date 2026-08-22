package http_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	requestsigninghttp "github.com/primandproper/platform-go/v13/cryptography/requestsigning/http"
)

// The middleware is the inbound half of httpclient.WithRequestSigning: one
// verifier, one key source, and no handler that has to remember to check
// anything.
func ExampleNewMiddleware() {
	// In a real service this comes from secrets, via
	// requestsigning.NewSecretKeySource.
	keys := requestsigning.StaticKeyring(requestsigning.Keyring{Current: []byte("the shared key")})

	verifier, err := requestsigning.NewVerifier(keys)
	if err != nil {
		panic(err)
	}

	mw, err := requestsigninghttp.NewMiddleware(verifier)
	if err != nil {
		panic(err)
	}

	// The handler reads the verified bytes, not the socket. It has no signature
	// checking of its own, which is the point.
	handler := mw(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			res.WriteHeader(http.StatusBadRequest)

			return
		}

		fmt.Println("handled:", string(body))
		res.WriteHeader(http.StatusNoContent)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	payload := `{"id":"order-7"}`

	signature, err := requestsigning.Sign(
		requestsigning.Keyring{Current: []byte("the shared key")},
		[]byte(payload), time.Now(),
	)
	if err != nil {
		panic(err)
	}

	fmt.Println("signed:  ", post(server.URL, payload, signature))
	fmt.Println("unsigned:", post(server.URL, payload, ""))

	// Output:
	// handled: {"id":"order-7"}
	// signed:   204
	// unsigned: 401
}

// post sends payload, with signature when there is one, and reports the status.
func post(url, payload, signature string) int {
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		panic(err)
	}

	if signature != "" {
		req.Header.Set(requestsigning.SignatureHeader, signature)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = res.Body.Close() }()

	return res.StatusCode
}
