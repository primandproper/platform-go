# Benchmarks

_Generated 2026-06-23 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 282 | 4176897 | 67064560 | 126 |
| Argon2Authenticator/PasswordMatches | 250 | 4515036 | 67062768 | 124 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 414,051 | 2956 | 4223 | 62 |
| JWTSigner/ParseToken | 346,382 | 3426 | 3560 | 74 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,241,199 | 943.9 | 960 | 15 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 676,031,265 | 1.653 | 0 | 0 |
| Bitmask/Has | 672,979,588 | 1.669 | 0 | 0 |
| Bitmask/Set | 766,631,715 | 1.649 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 1,728,588 | 698.4 | 416 | 4 |
| InMemoryCache/Set | 1,921,275 | 654.5 | 416 | 4 |

## cache/redis

> 🐳 Requires testcontainers — run with `RUN_CONTAINER_TESTS=true` (and a running Docker daemon).

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| RedisCache/Get | 4,455 | 266879 | 7465 | 164 |
| RedisCache/GetMany | 4,096 | 280226 | 21592 | 480 |
| RedisCache/Set | 4,693 | 273307 | 1930 | 30 |
| RedisCache/SetMany | 4,635 | 264960 | 4712 | 73 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 140,888,215 | 8.563 | 0 | 0 |
| SlotForKey/plain | 207,766,498 | 5.792 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 177,125,572 | 6.586 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 143,014,344 | 8.310 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 49,909 | 21289 | 2108605 | 15 |
| Compressor/s2/Decompress | 16,393 | 68724 | 1100641 | 11 |
| Compressor/zstd/Compress | 8,208 | 142276 | 2346784 | 49 |
| Compressor/zstd/Decompress | 63,646 | 17822 | 48349 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,102,537 | 1100 | 2432 | 9 |
| EncryptorDecryptor/Encrypt | 832,278 | 1385 | 2960 | 11 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 11,869,024 | 87.60 | 256 | 3 |
| SHA256Hasher_Hash/256B | 3,829,101 | 324.4 | 1920 | 4 |
| SHA256Hasher_Hash/4096B | 353,152 | 3572 | 28416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 377,488 | 3141 | 1657 | 24 |
| SQLiteClient/QueryRow | 221,451 | 4688 | 3433 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 813,901 | 1493 | 456 | 6 |

## distributedlock/redis

> 🐳 Requires testcontainers — run with `RUN_CONTAINER_TESTS=true` (and a running Docker daemon).

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| RedisLocker_AcquireRelease | 1,735 | 588334 | 1430 | 28 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,257,740 | 951.1 | 1336 | 12 |
| ServerEncoderDecoder/EncodeJSON | 1,638,943 | 729.0 | 448 | 5 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 24,011,564 | 46.01 | 24 | 1 |
| Validate | 100,000,000 | 11.47 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 632,015,396 | 1.871 | 0 | 0 |
| Numbers/Scale | 689,038,731 | 1.772 | 0 | 0 |
| Numbers/ScaleToYield | 680,961,817 | 1.858 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 567,304 | 2087 | 1248 | 14 |
| Generator/RawBytes32 | 865,693 | 1428 | 832 | 9 |

