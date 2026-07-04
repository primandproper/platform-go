# Benchmarks

_Generated 2026-07-04 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 246 | 4873648 | 67130309 | 128 |
| Argon2Authenticator/PasswordMatches | 206 | 13696334 | 67128379 | 126 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 247,148 | 5927 | 4048 | 67 |
| JWTSigner/ParseToken | 302,200 | 3929 | 3336 | 75 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,381,742 | 880.3 | 704 | 14 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 566,011,815 | 2.079 | 0 | 0 |
| Bitmask/Has | 551,001,501 | 2.105 | 0 | 0 |
| Bitmask/Set | 603,814,218 | 2.077 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 4,319,980 | 273.5 | 96 | 3 |
| InMemoryCache/Set | 4,393,711 | 274.9 | 96 | 3 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 100,000,000 | 11.01 | 0 | 0 |
| SlotForKey/plain | 178,245,153 | 6.704 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 151,609,284 | 7.893 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 126,008,604 | 9.565 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 53,565 | 22051 | 2108609 | 15 |
| Compressor/s2/Decompress | 14,781 | 80001 | 1100664 | 12 |
| Compressor/zstd/Compress | 6,645 | 182882 | 2346788 | 49 |
| Compressor/zstd/Decompress | 58,646 | 20955 | 48367 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,434,045 | 834.5 | 2168 | 8 |
| EncryptorDecryptor/Encrypt | 987,172 | 1181 | 2696 | 10 |

## cryptography/encryption/salsa20

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,000,000 | 1123 | 920 | 6 |
| EncryptorDecryptor/Encrypt | 819,880 | 1451 | 1264 | 7 |

## cryptography/hashing/adler32

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Adler32Hasher_Hash/16B | 75,402,536 | 14.18 | 8 | 1 |
| Adler32Hasher_Hash/256B | 17,831,613 | 70.07 | 8 | 1 |
| Adler32Hasher_Hash/4096B | 1,000,000 | 1127 | 8 | 1 |

## cryptography/hashing/crc64

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| CRC64Hasher_Hash/16B | 34,575,115 | 34.56 | 16 | 1 |
| CRC64Hasher_Hash/256B | 8,026,098 | 151.0 | 16 | 1 |
| CRC64Hasher_Hash/4096B | 615,121 | 1973 | 16 | 1 |

## cryptography/hashing/fnv

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| FNVHasher_Hash/16B | 16,890,454 | 65.64 | 32 | 1 |
| FNVHasher_Hash/256B | 1,483,138 | 791.2 | 32 | 1 |
| FNVHasher_Hash/4096B | 101,402 | 12212 | 32 | 1 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 14,743,304 | 82.77 | 160 | 3 |
| SHA256Hasher_Hash/256B | 6,600,217 | 207.9 | 416 | 4 |
| SHA256Hasher_Hash/4096B | 553,224 | 1841 | 4256 | 4 |

## cryptography/hashing/sha512

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA512Hasher_Hash/16B | 6,662,085 | 173.1 | 320 | 3 |
| SHA512Hasher_Hash/256B | 3,423,562 | 342.0 | 576 | 4 |
| SHA512Hasher_Hash/4096B | 418,570 | 2983 | 4416 | 4 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,237,608 | 966.3 | 224 | 7 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,827,978 | 637.8 | 1096 | 12 |
| ServerEncoderDecoder/EncodeJSON | 4,744,801 | 249.1 | 192 | 4 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 24,630,583 | 47.78 | 24 | 1 |
| Validate | 100,000,000 | 11.62 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 169,845,450 | 6.984 | 0 | 0 |
| Numbers/Scale | 155,661,270 | 7.680 | 0 | 0 |
| Numbers/ScaleToYield | 158,527,132 | 7.690 | 0 | 0 |

## observability/logging/slog

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlogLogger/Chained | 901,563 | 1390 | 1025 | 23 |
| SlogLogger/Error | 1,377,732 | 847.7 | 0 | 0 |
| SlogLogger/Info | 1,619,989 | 732.9 | 0 | 0 |
| SlogLogger/WithValue | 1,251,439 | 951.3 | 304 | 8 |
| SlogLogger/WithValues | 909,454 | 1394 | 933 | 20 |

## observability/logging/zap

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ZapLogger/Chained | 1,229,126 | 968.1 | 4362 | 24 |
| ZapLogger/Error | 14,516,794 | 82.40 | 70 | 1 |
| ZapLogger/Info | 22,843,219 | 53.13 | 2 | 0 |
| ZapLogger/WithValue | 3,286,266 | 357.1 | 1455 | 8 |
| ZapLogger/WithValues | 1,000,000 | 1060 | 4314 | 22 |

## observability/logging/zerolog

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ZerologLogger/Chained | 1,247,191 | 947.8 | 2147 | 10 |
| ZerologLogger/Error | 1,463,916 | 818.5 | 360 | 3 |
| ZerologLogger/Info | 2,183,067 | 536.4 | 0 | 0 |
| ZerologLogger/WithValue | 1,625,172 | 728.0 | 753 | 4 |
| ZerologLogger/WithValues | 1,000,000 | 1243 | 2516 | 11 |

## observability/metrics

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Float64Histogram/Record | 33,463,196 | 37.33 | 0 | 0 |
| Float64Histogram/RecordWithAttributes | 22,619,796 | 52.29 | 16 | 1 |
| Int64Counter/Add | 39,384,043 | 30.08 | 0 | 0 |
| Int64Counter/AddWithAttributes | 26,159,272 | 46.79 | 16 | 1 |
| NoopProvider/Add | 446,643,475 | 2.614 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 3,028,935 | 380.5 | 128 | 4 |
| Generator/RawBytes32 | 3,319,353 | 346.9 | 112 | 3 |

