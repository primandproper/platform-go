# Benchmarks

_Generated 2026-06-28 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 242 | 4851059 | 67064814 | 128 |
| Argon2Authenticator/PasswordMatches | 243 | 4958525 | 67062881 | 126 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 344,238 | 3479 | 4048 | 67 |
| JWTSigner/ParseToken | 323,467 | 3769 | 3304 | 73 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,373,744 | 888.1 | 704 | 14 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 567,274,076 | 2.101 | 0 | 0 |
| Bitmask/Has | 589,726,712 | 2.059 | 0 | 0 |
| Bitmask/Set | 567,450,112 | 2.098 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 4,611,820 | 273.0 | 96 | 3 |
| InMemoryCache/Set | 4,395,763 | 270.9 | 96 | 3 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 100,000,000 | 11.11 | 0 | 0 |
| SlotForKey/plain | 169,080,574 | 7.072 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 155,655,675 | 7.716 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 122,563,647 | 9.768 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 44,646 | 22416 | 2108605 | 15 |
| Compressor/s2/Decompress | 16,344 | 74915 | 1100641 | 11 |
| Compressor/zstd/Compress | 6,680 | 180441 | 2346788 | 49 |
| Compressor/zstd/Decompress | 53,977 | 21356 | 48376 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,440,294 | 837.4 | 2168 | 8 |
| EncryptorDecryptor/Encrypt | 910,680 | 1184 | 2696 | 10 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 13,022,637 | 92.58 | 256 | 3 |
| SHA256Hasher_Hash/256B | 3,566,943 | 341.0 | 1920 | 4 |
| SHA256Hasher_Hash/4096B | 326,730 | 3707 | 28416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 348,549 | 3171 | 1656 | 24 |
| SQLiteClient/QueryRow | 218,502 | 4899 | 3433 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,278,488 | 936.6 | 224 | 7 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,911,326 | 637.4 | 1096 | 12 |
| ServerEncoderDecoder/EncodeJSON | 4,651,404 | 252.0 | 192 | 4 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 22,254,848 | 48.16 | 24 | 1 |
| Validate | 100,000,000 | 11.50 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 617,397,622 | 1.858 | 0 | 0 |
| Numbers/Scale | 669,443,151 | 1.844 | 0 | 0 |
| Numbers/ScaleToYield | 669,867,924 | 1.869 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 3,084,594 | 384.1 | 128 | 4 |
| Generator/RawBytes32 | 3,301,735 | 359.8 | 112 | 3 |

