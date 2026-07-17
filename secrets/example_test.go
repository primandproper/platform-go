package secrets_test

import (
	"context"
	"fmt"
	"os"

	"github.com/primandproper/platform-go/v5/secrets/env"
)

func Example_envSecretSource() {
	os.Setenv("EXAMPLE_SECRET", "s3cret")
	defer os.Unsetenv("EXAMPLE_SECRET")

	source, err := env.NewEnvSecretSource(nil, nil, nil)
	if err != nil {
		panic(err)
	}
	defer source.Close()

	secret, err := source.GetSecret(context.Background(), "EXAMPLE_SECRET")
	if err != nil {
		panic(err)
	}

	fmt.Println(secret)
	// Output: s3cret
}
