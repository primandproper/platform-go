# Benchmarks

_Generated 2026-06-29 by `make bench`. Do not edit by hand — re-run to refresh._

**Environment:** goos `darwin` · goarch `arm64` · cpu `Apple M4 Max`

Times are nanoseconds per operation; lower is better. Run with `make bench` (set `RUN_CONTAINER_TESTS=true` to include infra-backed benchmarks).

## authentication/argon2

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Argon2Authenticator/HashPassword | 242 | 4847122 | 67064854 | 128 |
| Argon2Authenticator/PasswordMatches | 252 | 4991893 | 67062934 | 126 |

## authentication/tokens/jwt

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| JWTSigner/IssueToken | 355,456 | 3526 | 4049 | 67 |
| JWTSigner/ParseToken | 315,601 | 3787 | 3336 | 75 |

## authentication/totp

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Verifier_Verify | 1,392,454 | 864.9 | 704 | 14 |

## bitmask

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Bitmask/Count | 557,532,516 | 2.091 | 0 | 0 |
| Bitmask/Has | 580,127,724 | 2.053 | 0 | 0 |
| Bitmask/Set | 557,556,801 | 2.097 | 0 | 0 |

## cache/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| InMemoryCache/Get | 4,462,856 | 270.4 | 96 | 3 |
| InMemoryCache/Set | 4,375,972 | 267.4 | 96 | 3 |

## cache/redis/slots

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SlotForKey/hashtag | 100,000,000 | 10.93 | 0 | 0 |
| SlotForKey/plain | 170,703,993 | 7.002 | 0 | 0 |

## circuitbreaking/partitioned

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| KeyedCircuitBreaker/For_dedicated | 162,626,563 | 7.429 | 0 | 0 |
| KeyedCircuitBreaker/For_global | 126,143,834 | 9.396 | 0 | 0 |

## compression

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Compressor/s2/Compress | 45,321 | 22229 | 2108606 | 15 |
| Compressor/s2/Decompress | 12,458 | 96870 | 1100641 | 11 |
| Compressor/zstd/Compress | 6,867 | 173400 | 2346785 | 49 |
| Compressor/zstd/Decompress | 58,258 | 21455 | 48373 | 39 |

## cryptography/encryption/aes

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,568,707 | 741.3 | 2168 | 8 |
| EncryptorDecryptor/Encrypt | 1,000,000 | 1014 | 2696 | 10 |

## cryptography/encryption/salsa20

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| EncryptorDecryptor/Decrypt | 1,669,351 | 712.8 | 888 | 6 |
| EncryptorDecryptor/Encrypt | 1,432,822 | 823.3 | 1080 | 6 |

## cryptography/hashing/adler32

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Adler32Hasher_Hash/16B | 81,264,820 | 14.39 | 8 | 1 |
| Adler32Hasher_Hash/256B | 17,700,289 | 69.05 | 8 | 1 |
| Adler32Hasher_Hash/4096B | 1,000,000 | 1142 | 8 | 1 |

## cryptography/hashing/crc64

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| CRC64Hasher_Hash/16B | 32,592,576 | 36.44 | 16 | 1 |
| CRC64Hasher_Hash/256B | 7,201,148 | 154.9 | 16 | 1 |
| CRC64Hasher_Hash/4096B | 547,027 | 1955 | 16 | 1 |

## cryptography/hashing/fnv

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| FNVHasher_Hash/16B | 18,532,090 | 64.05 | 32 | 1 |
| FNVHasher_Hash/256B | 1,608,552 | 748.6 | 32 | 1 |
| FNVHasher_Hash/4096B | 102,333 | 11691 | 32 | 1 |

## cryptography/hashing/sha256

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA256Hasher_Hash/16B | 11,945,904 | 97.52 | 160 | 3 |
| SHA256Hasher_Hash/256B | 5,409,159 | 197.1 | 416 | 4 |
| SHA256Hasher_Hash/4096B | 737,660 | 1640 | 4256 | 4 |

## cryptography/hashing/sha512

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SHA512Hasher_Hash/16B | 6,476,818 | 173.5 | 320 | 3 |
| SHA512Hasher_Hash/256B | 3,790,699 | 335.3 | 576 | 4 |
| SHA512Hasher_Hash/4096B | 398,383 | 2733 | 4416 | 4 |

## database/sqlite

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SQLiteClient/Exec | 377,516 | 3216 | 1656 | 24 |
| SQLiteClient/QueryRow | 221,470 | 4705 | 3433 | 49 |

## distributedlock/memory

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Locker_AcquireRelease | 1,226,217 | 962.3 | 224 | 7 |

## encoding

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| ServerEncoderDecoder/DecodeBytes | 1,882,772 | 631.7 | 1096 | 12 |
| ServerEncoderDecoder/EncodeJSON | 4,452,454 | 255.5 | 192 | 4 |

## identifiers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| New | 22,256,190 | 46.72 | 24 | 1 |
| Validate | 100,000,000 | 11.55 | 0 | 0 |

## numbers

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Numbers/RoundToDecimalPlaces | 625,645,470 | 1.850 | 0 | 0 |
| Numbers/Scale | 674,432,948 | 1.816 | 0 | 0 |
| Numbers/ScaleToYield | 671,762,104 | 1.827 | 0 | 0 |

## random

| Benchmark | Runs | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| Generator/HexEncodedString16 | 2,969,534 | 389.9 | 128 | 4 |
| Generator/RawBytes32 | 3,382,892 | 357.3 | 112 | 3 |

