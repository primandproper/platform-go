package elasticsearch

import (
	"time"
)

type Config struct {
	Address               string        `env:"ADDRESS"                 json:"address"               yaml:"address"`
	Username              string        `env:"USERNAME"                json:"username"              yaml:"username"`
	Password              string        `env:"PASSWORD"                json:"password"              yaml:"password"`
	CACert                []byte        `env:"CA_CERT"                 json:"caCert"                yaml:"caCert"`
	IndexOperationTimeout time.Duration `env:"INDEX_OPERATION_TIMEOUT" json:"indexOperationTimeout" yaml:"indexOperationTimeout"`
}
