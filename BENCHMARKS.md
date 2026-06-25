# Benchmarks

_Generated 2026-06-24 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 283 | 4195597 | 67064226 | 124 |
| Argon2Authenticator/PasswordMatches | 301 | 4084529 | 67062481 | 122 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 485,875 | 2506 | 3934 | 60 |
| JWTSigner/ParseToken | 429,610 | 2867 | 3272 | 72 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,699,759 | 681.3 | 672 | 13 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 761,794,927 | 1.631 | 0 | 0 |
| Bitmask/Has | 651,748,713 | 1.705 | 0 | 0 |
| Bitmask/Set | 747,416,936 | 1.646 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 5,187,090 | 234.3 | 128 | 2 |
| InMemoryCache/Set | 5,647,922 | 216.4 | 128 | 2 |

## cache/redis

> 🐳 Requires testcontainers — run with `RUN_CONTAINER_TESTS=true` (and a running Docker daemon).

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| RedisCache/Get | 4,306 | 278206 | 7178 | 162 |
| RedisCache/GetMany | 4,856 | 278516 | 21305 | 478 |
| RedisCache/Set | 4,548 | 252446 | 1642 | 28 |
| RedisCache/SetMany | 5,280 | 261557 | 4424 | 71 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 140,796,492 | 8.587 | 0 | 0 |
| SlotForKey/plain | 198,220,761 | 6.087 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 182,820,427 | 6.629 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 146,930,881 | 8.138 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 46,682 | 21637 | 2108606 | 15 |
| Compressor/s2/Decompress | 19,496 | 61912 | 1100641 | 11 |
| Compressor/zstd/Compress | 8,085 | 145739 | 2346784 | 49 |
| Compressor/zstd/Decompress | 73,459 | 17762 | 48341 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,800,714 | 654.9 | 2144 | 7 |
| EncryptorDecryptor/Encrypt | 1,280,425 | 925.4 | 2672 | 9 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 11,567,640 | 88.72 | 256 | 3 |
| SHA256Hasher_Hash/256B | 3,805,171 | 327.1 | 1920 | 4 |
| SHA256Hasher_Hash/4096B | 338,434 | 3615 | 28416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 347,875 | 3197 | 1657 | 24 |
| SQLiteClient/QueryRow | 218,570 | 4942 | 3434 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,313,124 | 916.2 | 168 | 4 |

## distributedlock/redis

> 🐳 Requires testcontainers — run with `RUN_CONTAINER_TESTS=true` (and a running Docker daemon).

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| RedisLocker_AcquireRelease | 2,182 | 541700 | 845 | 24 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,956,217 | 593.7 | 1048 | 10 |
| ServerEncoderDecoder/EncodeJSON | 4,868,413 | 242.0 | 160 | 3 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 21,813,091 | 46.13 | 24 | 1 |
| Validate | 106,428,992 | 11.39 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 628,059,612 | 1.768 | 0 | 0 |
| Numbers/Scale | 696,706,490 | 1.790 | 0 | 0 |
| Numbers/ScaleToYield | 674,010,572 | 1.836 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 3,104,707 | 384.7 | 160 | 4 |
| Generator/RawBytes32 | 3,172,747 | 364.2 | 144 | 3 |

