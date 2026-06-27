# Benchmarks

_Generated 2026-06-27 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 262 | 4560706 | 67064357 | 125 |
| Argon2Authenticator/PasswordMatches | 292 | 4350001 | 67062517 | 123 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 447,920 | 2699 | 3968 | 61 |
| JWTSigner/ParseToken | 405,972 | 3004 | 3304 | 73 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,586,454 | 729.8 | 704 | 14 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 754,888,491 | 1.639 | 0 | 0 |
| Bitmask/Has | 666,415,064 | 1.665 | 0 | 0 |
| Bitmask/Set | 710,391,601 | 1.682 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 4,825,543 | 250.7 | 160 | 3 |
| InMemoryCache/Set | 5,086,266 | 242.6 | 160 | 3 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 124,842,910 | 9.555 | 0 | 0 |
| SlotForKey/plain | 203,813,780 | 5.921 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 177,289,622 | 6.467 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 144,342,206 | 8.280 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 52,561 | 20514 | 2108606 | 15 |
| Compressor/s2/Decompress | 15,829 | 69991 | 1100641 | 11 |
| Compressor/zstd/Compress | 8,565 | 144243 | 2346786 | 49 |
| Compressor/zstd/Decompress | 68,115 | 18996 | 48330 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,697,167 | 703.3 | 2176 | 8 |
| EncryptorDecryptor/Encrypt | 1,239,229 | 971.6 | 2704 | 10 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 11,386,387 | 89.17 | 256 | 3 |
| SHA256Hasher_Hash/256B | 3,098,113 | 343.3 | 1920 | 4 |
| SHA256Hasher_Hash/4096B | 328,219 | 3742 | 28416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 389,842 | 3157 | 1656 | 24 |
| SQLiteClient/QueryRow | 219,250 | 4698 | 3433 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,161,240 | 1027 | 360 | 10 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,905,219 | 625.1 | 1080 | 11 |
| ServerEncoderDecoder/EncodeJSON | 4,169,162 | 273.2 | 192 | 4 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 22,415,224 | 46.65 | 24 | 1 |
| Validate | 100,000,000 | 11.49 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 597,449,886 | 1.784 | 0 | 0 |
| Numbers/Scale | 676,648,326 | 1.763 | 0 | 0 |
| Numbers/ScaleToYield | 669,858,882 | 1.801 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 2,979,918 | 415.4 | 192 | 5 |
| Generator/RawBytes32 | 3,089,630 | 383.8 | 176 | 4 |

