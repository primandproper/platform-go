# Benchmarks

_Generated 2026-06-29 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 289 | 4282178 | 67064620 | 128 |
| Argon2Authenticator/PasswordMatches | 282 | 4408639 | 67062869 | 126 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 360,670 | 2860 | 4048 | 67 |
| JWTSigner/ParseToken | 397,622 | 3162 | 3336 | 75 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,555,112 | 746.0 | 704 | 14 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 676,169,191 | 1.779 | 0 | 0 |
| Bitmask/Has | 621,770,382 | 1.777 | 0 | 0 |
| Bitmask/Set | 634,685,706 | 1.781 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 4,925,192 | 237.8 | 96 | 3 |
| InMemoryCache/Set | 5,380,477 | 232.2 | 96 | 3 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 136,478,582 | 8.771 | 0 | 0 |
| SlotForKey/plain | 199,307,654 | 5.865 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 172,567,374 | 6.629 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 146,901,015 | 8.138 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 55,514 | 21195 | 2108605 | 15 |
| Compressor/s2/Decompress | 17,894 | 65120 | 1100642 | 11 |
| Compressor/zstd/Compress | 7,489 | 158798 | 2346784 | 49 |
| Compressor/zstd/Decompress | 68,828 | 18140 | 48351 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,800,418 | 687.3 | 2168 | 8 |
| EncryptorDecryptor/Encrypt | 1,200,716 | 1003 | 2696 | 10 |

## cryptography/encryption/salsa20

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,616,716 | 728.1 | 888 | 6 |
| EncryptorDecryptor/Encrypt | 1,442,635 | 809.4 | 1080 | 6 |

## cryptography/hashing/adler32

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Adler32Hasher_Hash/16B | 82,184,755 | 14.16 | 8 | 1 |
| Adler32Hasher_Hash/256B | 14,520,746 | 68.92 | 8 | 1 |
| Adler32Hasher_Hash/4096B | 1,000,000 | 1115 | 8 | 1 |

## cryptography/hashing/crc64

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| CRC64Hasher_Hash/16B | 32,951,942 | 36.00 | 16 | 1 |
| CRC64Hasher_Hash/256B | 8,053,616 | 149.9 | 16 | 1 |
| CRC64Hasher_Hash/4096B | 648,831 | 1939 | 16 | 1 |

## cryptography/hashing/fnv

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| FNVHasher_Hash/16B | 19,043,089 | 64.72 | 32 | 1 |
| FNVHasher_Hash/256B | 1,622,784 | 745.6 | 32 | 1 |
| FNVHasher_Hash/4096B | 106,627 | 11464 | 32 | 1 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 12,326,598 | 81.77 | 160 | 3 |
| SHA256Hasher_Hash/256B | 6,935,228 | 179.9 | 416 | 4 |
| SHA256Hasher_Hash/4096B | 774,494 | 1605 | 4256 | 4 |

## cryptography/hashing/sha512

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA512Hasher_Hash/16B | 6,547,975 | 169.6 | 320 | 3 |
| SHA512Hasher_Hash/256B | 3,852,680 | 320.1 | 576 | 4 |
| SHA512Hasher_Hash/4096B | 462,332 | 2759 | 4416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 396,421 | 3179 | 1656 | 24 |
| SQLiteClient/QueryRow | 220,098 | 4741 | 3433 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,281,457 | 925.9 | 224 | 7 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,890,136 | 635.8 | 1096 | 12 |
| ServerEncoderDecoder/EncodeJSON | 4,642,903 | 257.9 | 192 | 4 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 22,119,033 | 47.23 | 24 | 1 |
| Validate | 100,000,000 | 11.85 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 602,488,653 | 1.846 | 0 | 0 |
| Numbers/Scale | 664,690,441 | 1.827 | 0 | 0 |
| Numbers/ScaleToYield | 669,957,519 | 1.844 | 0 | 0 |

## observability/logging/slog

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlogLogger/Chained | 790,969 | 1329 | 1025 | 23 |
| SlogLogger/Error | 1,309,820 | 919.3 | 80 | 3 |
| SlogLogger/Info | 1,645,893 | 712.2 | 0 | 0 |
| SlogLogger/WithValue | 1,291,554 | 942.5 | 304 | 8 |
| SlogLogger/WithValues | 945,237 | 1336 | 933 | 20 |

## observability/logging/zap

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ZapLogger/Chained | 1,000,000 | 1057 | 4362 | 24 |
| ZapLogger/Error | 7,516,696 | 157.6 | 86 | 3 |
| ZapLogger/Info | 22,095,117 | 53.77 | 2 | 0 |
| ZapLogger/WithValue | 3,119,892 | 357.3 | 1455 | 8 |
| ZapLogger/WithValues | 1,266,108 | 957.0 | 4313 | 22 |

## observability/logging/zerolog

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ZerologLogger/Chained | 1,220,828 | 973.8 | 2147 | 10 |
| ZerologLogger/Error | 1,291,371 | 922.1 | 416 | 7 |
| ZerologLogger/Info | 2,286,027 | 526.8 | 0 | 0 |
| ZerologLogger/WithValue | 1,671,169 | 702.3 | 753 | 4 |
| ZerologLogger/WithValues | 1,000,000 | 1128 | 2516 | 11 |

## observability/metrics

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Float64Histogram/Record | 33,605,991 | 37.23 | 0 | 0 |
| Float64Histogram/RecordWithAttributes | 23,104,063 | 53.38 | 16 | 1 |
| Int64Counter/Add | 39,762,998 | 30.41 | 0 | 0 |
| Int64Counter/AddWithAttributes | 26,496,792 | 45.41 | 16 | 1 |
| NoopProvider/Add | 474,677,362 | 2.542 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 2,969,828 | 387.4 | 128 | 4 |
| Generator/RawBytes32 | 3,294,121 | 358.5 | 112 | 3 |

