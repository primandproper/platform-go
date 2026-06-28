# Benchmarks

_Generated 2026-06-28 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 240 | 4810521 | 67064797 | 128 |
| Argon2Authenticator/PasswordMatches | 249 | 4829089 | 67062915 | 126 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 339,550 | 3458 | 4049 | 67 |
| JWTSigner/ParseToken | 333,427 | 3703 | 3304 | 73 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,381,140 | 871.4 | 704 | 14 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 550,919,078 | 2.105 | 0 | 0 |
| Bitmask/Has | 556,690,520 | 2.088 | 0 | 0 |
| Bitmask/Set | 565,287,020 | 2.100 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 4,574,421 | 267.7 | 96 | 3 |
| InMemoryCache/Set | 4,551,736 | 268.1 | 96 | 3 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 100,000,000 | 10.90 | 0 | 0 |
| SlotForKey/plain | 169,946,806 | 6.974 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 162,285,090 | 7.401 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 123,085,312 | 9.659 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 53,744 | 22160 | 2108607 | 15 |
| Compressor/s2/Decompress | 16,608 | 73337 | 1100641 | 11 |
| Compressor/zstd/Compress | 5,792 | 196226 | 2346786 | 49 |
| Compressor/zstd/Decompress | 54,822 | 21127 | 48362 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,435,825 | 835.7 | 2168 | 8 |
| EncryptorDecryptor/Encrypt | 942,944 | 1179 | 2696 | 10 |

## cryptography/encryption/salsa20

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,413,728 | 832.2 | 888 | 6 |
| EncryptorDecryptor/Encrypt | 1,234,651 | 970.7 | 1080 | 6 |

## cryptography/hashing/adler32

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Adler32Hasher_Hash/16B | 83,364,111 | 14.42 | 8 | 1 |
| Adler32Hasher_Hash/256B | 17,751,271 | 68.35 | 8 | 1 |
| Adler32Hasher_Hash/4096B | 1,000,000 | 1128 | 8 | 1 |

## cryptography/hashing/crc64

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| CRC64Hasher_Hash/16B | 32,025,465 | 37.02 | 16 | 1 |
| CRC64Hasher_Hash/256B | 7,383,433 | 150.8 | 16 | 1 |
| CRC64Hasher_Hash/4096B | 625,832 | 2046 | 16 | 1 |

## cryptography/hashing/fnv

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| FNVHasher_Hash/16B | 18,338,418 | 64.67 | 32 | 1 |
| FNVHasher_Hash/256B | 1,583,032 | 754.0 | 32 | 1 |
| FNVHasher_Hash/4096B | 102,974 | 11999 | 32 | 1 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 13,245,610 | 81.35 | 160 | 3 |
| SHA256Hasher_Hash/256B | 6,008,397 | 180.9 | 416 | 4 |
| SHA256Hasher_Hash/4096B | 736,102 | 1651 | 4256 | 4 |

## cryptography/hashing/sha512

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA512Hasher_Hash/16B | 6,483,416 | 173.5 | 320 | 3 |
| SHA512Hasher_Hash/256B | 3,770,580 | 339.5 | 576 | 4 |
| SHA512Hasher_Hash/4096B | 425,053 | 2729 | 4416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 390,013 | 3165 | 1656 | 24 |
| SQLiteClient/QueryRow | 253,417 | 4701 | 3433 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,231,582 | 964.3 | 224 | 7 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,911,274 | 623.6 | 1096 | 12 |
| ServerEncoderDecoder/EncodeJSON | 4,610,078 | 256.7 | 192 | 4 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 25,873,035 | 46.91 | 24 | 1 |
| Validate | 100,000,000 | 11.57 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 621,328,392 | 1.835 | 0 | 0 |
| Numbers/Scale | 671,961,946 | 1.818 | 0 | 0 |
| Numbers/ScaleToYield | 665,592,780 | 1.831 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 2,957,844 | 385.0 | 128 | 4 |
| Generator/RawBytes32 | 3,351,794 | 357.5 | 112 | 3 |

