# Benchmarks

_Generated 2026-06-28 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 231 | 5004240 | 67064830 | 128 |
| Argon2Authenticator/PasswordMatches | 248 | 4969496 | 67062901 | 126 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 343,694 | 3512 | 4049 | 67 |
| JWTSigner/ParseToken | 321,855 | 3808 | 3304 | 73 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,370,960 | 873.8 | 704 | 14 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 599,992,748 | 2.056 | 0 | 0 |
| Bitmask/Has | 560,206,435 | 2.100 | 0 | 0 |
| Bitmask/Set | 581,348,874 | 2.088 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 4,376,578 | 268.0 | 96 | 3 |
| InMemoryCache/Set | 4,578,688 | 267.5 | 96 | 3 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 121,177,639 | 9.978 | 0 | 0 |
| SlotForKey/plain | 177,475,324 | 6.697 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 164,392,690 | 7.400 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 125,926,791 | 9.452 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 51,315 | 22918 | 2108606 | 15 |
| Compressor/s2/Decompress | 15,669 | 77211 | 1100641 | 11 |
| Compressor/zstd/Compress | 6,402 | 183141 | 2346785 | 49 |
| Compressor/zstd/Decompress | 59,487 | 21227 | 48377 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,771,216 | 690.0 | 2168 | 8 |
| EncryptorDecryptor/Encrypt | 940,610 | 1144 | 2696 | 10 |

## cryptography/encryption/salsa20

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,595,976 | 752.1 | 888 | 6 |
| EncryptorDecryptor/Encrypt | 1,448,792 | 823.2 | 1080 | 6 |

## cryptography/hashing/adler32

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Adler32Hasher_Hash/16B | 84,661,034 | 14.33 | 8 | 1 |
| Adler32Hasher_Hash/256B | 17,276,959 | 68.98 | 8 | 1 |
| Adler32Hasher_Hash/4096B | 1,000,000 | 1115 | 8 | 1 |

## cryptography/hashing/crc64

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| CRC64Hasher_Hash/16B | 32,661,882 | 36.22 | 16 | 1 |
| CRC64Hasher_Hash/256B | 8,014,648 | 152.8 | 16 | 1 |
| CRC64Hasher_Hash/4096B | 624,901 | 2001 | 16 | 1 |

## cryptography/hashing/fnv

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| FNVHasher_Hash/16B | 18,512,269 | 64.26 | 32 | 1 |
| FNVHasher_Hash/256B | 1,586,827 | 752.3 | 32 | 1 |
| FNVHasher_Hash/4096B | 101,407 | 11771 | 32 | 1 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 14,100,630 | 83.73 | 160 | 3 |
| SHA256Hasher_Hash/256B | 6,025,851 | 190.4 | 416 | 4 |
| SHA256Hasher_Hash/4096B | 760,374 | 1602 | 4256 | 4 |

## cryptography/hashing/sha512

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA512Hasher_Hash/16B | 5,874,649 | 178.0 | 320 | 3 |
| SHA512Hasher_Hash/256B | 3,818,132 | 330.5 | 576 | 4 |
| SHA512Hasher_Hash/4096B | 429,886 | 2841 | 4416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 376,338 | 3170 | 1656 | 24 |
| SQLiteClient/QueryRow | 214,306 | 4704 | 3433 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,305,079 | 910.4 | 224 | 7 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,799,420 | 659.1 | 1096 | 12 |
| ServerEncoderDecoder/EncodeJSON | 4,389,758 | 253.7 | 192 | 4 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 22,220,095 | 46.58 | 24 | 1 |
| Validate | 100,000,000 | 11.62 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 680,643,973 | 1.798 | 0 | 0 |
| Numbers/Scale | 676,010,475 | 1.793 | 0 | 0 |
| Numbers/ScaleToYield | 519,505,257 | 1.939 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 3,081,217 | 380.5 | 128 | 4 |
| Generator/RawBytes32 | 3,427,852 | 357.0 | 112 | 3 |

