# Immutable volume distribution contract v1

Studio publishes portable file trees. Consumers retain verified bytes and runtime
metadata in their own storage. No descriptor contains a host path, engine handle,
account secret, executable mode, or runtime/system capability.

The Studio and Aster Go implementations are independently compiled and validated
against these fixtures. They do not import each other's business packages. The
Node test implements the encoding separately to catch cross-language drift.

## Canonical descriptor

The format discriminator is `volume-manifest-v1`; the content profile is
`authored-v1`. The fields are exactly those in the [fixtures](fixtures/index.json).
Every field is required, including empty directory digests and zero sizes.
Object keys occur in ascending ASCII order; entries occur in ascending UTF-8 path
byte order. Capabilities are a sorted unique nonempty subset of `data, workspace`.
IDs contain 1–128 ASCII letters, digits, hyphens or underscores.

Encoding is UTF-8 JSON without insignificant whitespace or a trailing newline.
Strings use JSON short escapes for quote, backslash, backspace, tab, newline,
form-feed and carriage return. Other U+0000–U+001F characters, `<`, `>`, `&`,
U+2028 and U+2029 use lowercase `\uXXXX` escapes. All other characters remain
literal UTF-8; slash is unescaped. No Unicode normalization occurs. Numbers are
canonical decimal integers; all **byte counts** are strings containing unsigned
decimal digits with no leading zeros (except `"0"`).

The descriptor digest is SHA-256 of `volume-manifest-v1`, one NUL byte, then the
exact canonical JSON bytes, represented as `sha256:` plus lowercase hexadecimal.
This digest differs from the tree digest. A consumer rejects any noncanonical
wire representation, including duplicate keys, unknown/missing fields, invalid
UTF-8 and alternate escapes. Unsupported versions fail explicitly.

The tree digest preserves Aster's content identity: construct one record per entry
as `kind + NUL + path + NUL + size + NUL + digest + LF`, using `d` for directories
and `f` for files. Sort complete records by UTF-8 bytes, concatenate, then SHA-256.
Directories have size `"0"` and digest `""`. Files retain their whole-file SHA-256,
including the SHA-256 of zero bytes for empty files. Labels are not identities.

## Profile and path validation

The profile permits at most 256 MiB/file, 1 GiB/tree, 10,000 entries, 512 UTF-8
bytes/path, 16 path components, and 16 MiB encoded descriptor bytes. These are
protocol bounds; production capability advertisement additionally requires the
end-to-end qualification described in the [delivery tracker](../../../docs/volume-studio-delivery.md).

Explicit directories, including empty directories, are required. A tree must
contain at least one entry. Parents must exist as directories. Paths must be
clean, relative, unique and valid UTF-8; traversal, backslashes, control
characters, symlinks and special entries are refused. Directory components
cannot have leading/trailing Unicode whitespace. File leaf names may contain
ordinary leading/trailing spaces. Spelling and case are preserved.

The receiver independently checks paths, parents, count, aggregate bytes, file
digests, tree digest and supported capabilities before admitting transfer or
allocating engine capacity. Capability metadata never grants system privileges.

## Checks

```sh
go -C volumes-studio/backend test ./...
go -C orchestrator test ./internal/volumeimport/manifest
node --test volumes-studio/tests/contract/*.test.mjs
```
