// Package totpmock provides moq-generated mock implementations of interfaces in
// the totp package.
package totpmock

// Regenerate via `go generate ./authentication/totp/mock/`.

//go:generate go tool github.com/matryer/moq -out totp_mock.go -pkg totpmock -rm -fmt goimports .. Verifier:VerifierMock Generator:GeneratorMock
